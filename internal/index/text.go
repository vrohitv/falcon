package index

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/gob"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The text index: for every trigram (three bytes, lowercased), the files that contain it. A search for
// "handleRequest" only needs the files that contain every trigram of it, usually a handful, and git grep
// then reads just those. The index only ever proposes candidates; git grep decides what matches, so a
// result is never wrong, only faster.
//
// It is kept small: each trigram's files are sorted numbers stored as varint gaps (about a byte and a
// half each instead of four), all in one buffer found through a sorted table of trigrams. A file's own
// trigram set is kept only while the file has changed since the last build (it then decides that file
// alone), so the postings exist once. A rebuild starts from the previous index: the changed and deleted
// files' entries go, the changed files' new trigrams come in.
//
// It stays correct as files change: before answering, the listing and file times are checked, and any
// file that changed since the index was built is read again (and decided by its own trigrams) until the
// next rebuild.

const (
	maxTextFile = 1 << 20  // larger files are not indexed (and so are always searched)
	maxPostings = 60 << 20 // beyond this many file-trigram pairs, the folder is searched without the index
)

type textFile struct {
	size, mod int64
	grams     []uint32 // sorted, unique; only while not yet in the inverted index
	skip      bool     // not indexed (too large, unreadable, binary): always a candidate
}

// postings is the inverted index: grams[i]'s files are the varint gaps in blob[offs[i]:offs[i+1]].
type postings struct {
	grams []uint32
	offs  []uint32
	blob  []byte
}

func (p *postings) list(g uint32) []byte {
	i := sort.Search(len(p.grams), func(i int) bool { return p.grams[i] >= g })
	if i == len(p.grams) || p.grams[i] != g {
		return nil
	}
	return p.blob[p.offs[i]:p.offs[i+1]]
}

// decode appends a list's file numbers.
func decode(dst []int32, b []byte) []int32 {
	prev := int32(-1)
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		prev += int32(v) + 1
		dst = append(dst, prev)
		b = b[n:]
	}
	return dst
}

// encode appends sorted file numbers as varint gaps.
func encode(dst []byte, ids []int32) []byte {
	prev := int32(-1)
	for _, id := range ids {
		dst = binary.AppendUvarint(dst, uint64(id-prev-1))
		prev = id
	}
	return dst
}

type textIndex struct {
	mu       sync.Mutex
	files    map[string]*textFile
	ids      []string // file numbers, by path
	inv      *postings
	skipped  []string // files never indexed: always candidates
	built    bool
	pending  map[string]bool // changed (or removed) since the inverted index was built
	statAt   time.Time
	postings int
	disabled bool
	building bool
	loaded   bool // the saved copy was read (or there was none)
}

func gramsOf(b []byte) []uint32 {
	if len(b) < 3 {
		return nil
	}
	seen := make(map[uint32]struct{}, min(len(b), 4096))
	var a, c, d byte
	for i, ch := range b {
		if 'A' <= ch && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		a, c, d = c, d, ch
		if i >= 2 {
			seen[uint32(a)<<16|uint32(c)<<8|uint32(d)] = struct{}{}
		}
	}
	out := make([]uint32, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (x *Index) readText(path string) *textFile {
	abs := filepath.Join(x.repo.Path, path)
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return &textFile{skip: true}
	}
	tf := &textFile{size: info.Size(), mod: info.ModTime().UnixNano()}
	if info.Size() > maxTextFile {
		tf.skip = true
		return tf
	}
	b, err := x.repo.ReadText(path)
	if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		tf.skip = true
		return tf
	}
	tf.grams = gramsOf(b)
	return tf
}

// syncText brings the file list up to date with the disk, reading only what changed (on every core).
// With rebuild, the inverted index is rebuilt too.
func (x *Index) syncText(ctx context.Context, files []string, rebuild bool) {
	t := &x.text
	t.mu.Lock()
	if t.files == nil {
		t.files = map[string]*textFile{}
		t.pending = map[string]bool{}
	}
	old := t.files
	t.mu.Unlock()
	next := make(map[string]*textFile, len(files))
	changed := []string{}
	for _, p := range files {
		if ctx.Err() != nil {
			return
		}
		info, err := os.Lstat(filepath.Join(x.repo.Path, p))
		if err != nil {
			continue
		}
		if prev, ok := old[p]; ok && prev.size == info.Size() && prev.mod == info.ModTime().UnixNano() {
			next[p] = prev
			continue
		}
		changed = append(changed, p)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan string)
	for w := 0; w < workers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				tf := x.readText(p)
				mu.Lock()
				next[p] = tf
				mu.Unlock()
			}
		}()
	}
	for _, p := range changed {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.files = next
	t.statAt = time.Now()
	for _, p := range changed {
		t.pending[p] = true
	}
	for p := range old {
		if _, ok := next[p]; !ok {
			t.pending[p] = true // removed: dropped from answers until the next rebuild
		}
	}
	if rebuild && (len(t.pending) > 0 || !t.built) {
		t.rebuildLocked()
	}
}

// rebuildLocked makes the inverted index match the files: from scratch the first time, otherwise from
// the previous index (entries of changed and removed files dropped, changed files' trigrams added).
func (t *textIndex) rebuildLocked() {
	ids := make([]string, 0, len(t.files))
	skipped := []string{}
	for p, tf := range t.files {
		if tf.skip {
			skipped = append(skipped, p)
			continue
		}
		ids = append(ids, p)
	}
	sort.Strings(ids)
	sort.Strings(skipped)
	newID := make(map[string]int32, len(ids))
	for i, p := range ids {
		newID[p] = int32(i)
	}
	lists := map[uint32][]int32{}
	add := func(id int32, grams []uint32) {
		for _, g := range grams {
			lists[g] = append(lists[g], id)
		}
	}
	total := 0
	if t.built && t.inv != nil {
		// Old file numbers that survive unchanged, renumbered (both numberings follow path order, so a
		// renumbered list stays sorted).
		remap := make([]int32, len(t.ids))
		for i, p := range t.ids {
			remap[i] = -1
			if id, ok := newID[p]; ok && !t.pending[p] {
				remap[i] = id
			}
		}
		// The changed files' trigrams, by trigram.
		for p := range t.pending {
			if tf := t.files[p]; tf != nil && !tf.skip {
				add(newID[p], tf.grams)
			}
		}
		for _, l := range lists {
			sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
		}
		next := &postings{offs: []uint32{0}}
		var buf, old []int32
		// Every trigram of the old index (merged with the changed files' for the same trigram), then the
		// trigrams new to it, in order.
		fresh := make([]uint32, 0, len(lists))
		for g := range lists {
			if t.inv.list(g) == nil {
				fresh = append(fresh, g)
			}
		}
		sort.Slice(fresh, func(i, j int) bool { return fresh[i] < fresh[j] })
		emit := func(g uint32, l []int32) {
			if len(l) == 0 {
				return
			}
			next.grams = append(next.grams, g)
			next.blob = encode(next.blob, l)
			next.offs = append(next.offs, uint32(len(next.blob)))
			total += len(l)
		}
		oi, fi := 0, 0
		for oi < len(t.inv.grams) || fi < len(fresh) {
			if fi == len(fresh) || (oi < len(t.inv.grams) && t.inv.grams[oi] < fresh[fi]) {
				g := t.inv.grams[oi]
				old = decode(old[:0], t.inv.blob[t.inv.offs[oi]:t.inv.offs[oi+1]])
				buf = buf[:0]
				for _, id := range old {
					if r := remap[id]; r >= 0 {
						buf = append(buf, r)
					}
				}
				if extra := lists[g]; len(extra) > 0 {
					buf = mergeSorted(buf, extra)
				}
				emit(g, buf)
				oi++
			} else {
				emit(fresh[fi], lists[fresh[fi]])
				fi++
			}
		}
		t.inv = next
	} else {
		// From scratch: each file's trigrams go into the lists (and are let go below).
		for i, p := range ids {
			tf := t.files[p]
			add(int32(i), tf.grams)
			total += len(tf.grams)
			tf.grams = nil
			if total > maxPostings {
				break
			}
		}
		if total <= maxPostings {
			grams := make([]uint32, 0, len(lists))
			for g := range lists {
				grams = append(grams, g)
			}
			sort.Slice(grams, func(i, j int) bool { return grams[i] < grams[j] })
			inv := &postings{grams: grams, offs: make([]uint32, 1, len(grams)+1), blob: make([]byte, 0, total+total/2)}
			for _, g := range grams {
				inv.blob = encode(inv.blob, lists[g]) // ids were added in order
				inv.offs = append(inv.offs, uint32(len(inv.blob)))
				delete(lists, g)
			}
			inv.blob = append([]byte(nil), inv.blob...) // exactly its size
			t.inv = inv
		}
	}
	t.postings = total
	if total > maxPostings {
		t.disabled, t.built, t.inv, t.ids = true, false, nil, nil
		return
	}
	// In the index now: the files' own trigram sets go.
	for _, tf := range t.files {
		tf.grams = nil
	}
	t.ids, t.skipped, t.built, t.disabled = ids, skipped, true, false
	t.pending = map[string]bool{}
}

func mergeSorted(a, b []int32) []int32 {
	out := make([]int32, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || (i < len(a) && a[i] < b[j]):
			out = append(out, a[i])
			i++
		case i == len(a) || b[j] < a[i]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

// BuildText indexes the folder's text in the background (once at a time).
func (x *Index) BuildText() {
	t := &x.text
	t.mu.Lock()
	// Built, nothing pending and checked a moment ago: nothing to do.
	if t.building || (t.built && len(t.pending) == 0 && time.Since(t.statAt) < 10*time.Second) {
		t.mu.Unlock()
		return
	}
	t.building = true
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			t.building = false
			t.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// First the copy saved last time: only what changed since is read again.
		x.loadText()
		files, err := x.Fresh(ctx)
		if err != nil {
			return
		}
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		x.syncText(ctx, paths, true)
		x.saveText()
	}()
}

// The text index on disk, as it is in memory: the files' sizes and times (so a restart reads only what
// changed while Falcon was stopped) and the compressed inverted index.
type savedText struct {
	Version int
	Files   map[string]savedTextFile
	IDs     []string
	Grams   []uint32
	Offs    []uint32
	Blob    []byte
}
type savedTextFile struct {
	Size, Mod int64
	Skip      bool
}

const savedTextVersion = 2

func (x *Index) textFile() string {
	if x.save == "" {
		return ""
	}
	return strings.TrimSuffix(x.save, ".gob") + ".text.gob"
}

func (x *Index) loadText() {
	t := &x.text
	t.mu.Lock()
	if t.loaded {
		t.mu.Unlock()
		return
	}
	t.loaded = true
	t.mu.Unlock()
	name := x.textFile()
	if name == "" {
		return
	}
	f, err := os.Open(name)
	if err != nil {
		return
	}
	defer f.Close()
	var sv savedText
	if gob.NewDecoder(bufio.NewReader(f)).Decode(&sv) != nil || sv.Version != savedTextVersion || len(sv.Offs) != len(sv.Grams)+1 {
		return
	}
	files := make(map[string]*textFile, len(sv.Files))
	skipped := []string{}
	for p, tf := range sv.Files {
		files[p] = &textFile{size: tf.Size, mod: tf.Mod, skip: tf.Skip}
		if tf.Skip {
			skipped = append(skipped, p)
		}
	}
	sort.Strings(skipped)
	t.mu.Lock()
	if len(t.files) == 0 {
		t.files, t.ids, t.skipped = files, sv.IDs, skipped
		t.inv = &postings{grams: sv.Grams, offs: sv.Offs, blob: sv.Blob}
		t.pending = map[string]bool{}
		t.built = true
	}
	t.mu.Unlock()
}

func (x *Index) saveText() {
	name := x.textFile()
	if name == "" || x.closed.Load() {
		return
	}
	t := &x.text
	t.mu.Lock()
	if !t.built || t.inv == nil || len(t.pending) > 0 {
		t.mu.Unlock()
		return
	}
	sv := savedText{Version: savedTextVersion, Files: make(map[string]savedTextFile, len(t.files)), IDs: t.ids, Grams: t.inv.grams, Offs: t.inv.offs, Blob: t.inv.blob}
	for p, tf := range t.files {
		sv.Files[p] = savedTextFile{Size: tf.size, Mod: tf.mod, Skip: tf.skip}
	}
	t.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return
	}
	tmp := name + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	if gob.NewEncoder(w).Encode(sv) != nil || w.Flush() != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	if x.closed.Load() {
		os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, name)
}

// TextReady reports whether searches can use the text index.
func (x *Index) TextReady() bool {
	x.text.mu.Lock()
	defer x.text.mu.Unlock()
	return x.text.built
}

// Candidates are the files that can contain q (a literal, matched with or without case), or ok=false when
// the index cannot answer (not built yet, too short a query, too large a folder): then search everything.
// The listing and file times are checked first, so a file changed a moment ago is never missed.
func (x *Index) Candidates(ctx context.Context, q string) ([]string, bool) {
	t := &x.text
	t.mu.Lock()
	ready := t.built && !t.disabled
	t.mu.Unlock()
	if !ready || len(q) < 3 {
		if !ready {
			x.BuildText()
		}
		return nil, false
	}
	// Checked before every answer: a file another tool wrote a moment ago is never missed. The check is
	// the listing plus one stat per file; only files that changed are read.
	x.mu.Lock()
	x.dirty = true
	x.mu.Unlock()
	files, err := x.Fresh(ctx)
	if err != nil {
		return nil, false
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	x.syncText(ctx, paths, false)
	// Many changes: rebuild the inverted index in the background.
	t.mu.Lock()
	many := len(t.pending) > 256
	t.mu.Unlock()
	if many {
		x.BuildText()
	}
	// Only trigrams of plain ASCII are compared: case folding beyond ASCII (Ü and ü) is not the same
	// bytes, and leaving those out can only add candidates, never lose one.
	grams := []uint32{}
	for _, g := range gramsOf([]byte(q)) {
		if g&0x808080 == 0 {
			grams = append(grams, g)
		}
	}
	if len(grams) == 0 {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.built || t.inv == nil {
		return nil, false
	}
	// The shortest list first, then each other list narrows it.
	lists := make([][]byte, 0, len(grams))
	for _, g := range grams {
		lists = append(lists, t.inv.list(g))
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	ids := decode(nil, lists[0])
	for _, l := range lists[1:] {
		if len(ids) == 0 {
			break
		}
		ids = intersectEncoded(ids, l)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids)+len(t.pending))
	for _, id := range ids {
		p := t.ids[id]
		// A file that changed since the build is decided by its current trigrams below.
		if t.pending[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	for p := range t.pending {
		tf := t.files[p]
		if tf == nil {
			continue // deleted: nothing to find
		}
		if tf.skip || containsAll(tf.grams, grams) {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	// Files never indexed (large, binary to the reader, unreadable) are always searched.
	for _, p := range t.skipped {
		if tf := t.files[p]; tf != nil && tf.skip && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, true
}

// intersectEncoded keeps the ids that also appear in an encoded list (decoded as it is walked).
func intersectEncoded(ids []int32, b []byte) []int32 {
	out := ids[:0]
	i := 0
	prev := int32(-1)
	for len(b) > 0 && i < len(ids) {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		prev += int32(v) + 1
		b = b[n:]
		for i < len(ids) && ids[i] < prev {
			i++
		}
		if i < len(ids) && ids[i] == prev {
			out = append(out, prev)
			i++
		}
	}
	return out
}

func containsAll(have, want []uint32) bool {
	for _, g := range want {
		i := sort.Search(len(have), func(i int) bool { return have[i] >= g })
		if i == len(have) || have[i] != g {
			return false
		}
	}
	return true
}

// TextStats describes the text index, for status.
type TextStats struct {
	Ready    bool `json:"ready"`
	Files    int  `json:"files"`
	Postings int  `json:"postings"`
	Disabled bool `json:"disabled"`
}

// TextStatus reports how the text index stands.
func (x *Index) TextStatus() TextStats {
	t := &x.text
	t.mu.Lock()
	defer t.mu.Unlock()
	return TextStats{Ready: t.built, Files: len(t.files), Postings: t.postings, Disabled: t.disabled}
}
