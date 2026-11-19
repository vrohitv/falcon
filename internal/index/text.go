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

// The text index: for every text file, the set of three-byte sequences (trigrams, lowercased) it contains,
// and for every trigram, the files that contain it. A search for "handleRequest" only needs the files that
// contain every trigram of it, usually a handful, and git grep then reads just those. The index only ever
// proposes candidates; git grep decides what matches, so a result is never wrong, only faster.
//
// It stays correct as files change: before answering, the listing and file times are checked, and any
// file that changed since the index was built is read again (and always searched) until the next rebuild.

const (
	maxTextFile = 1 << 20  // larger files are not indexed (and so are always searched)
	maxPostings = 60 << 20 // beyond this many file-trigram pairs, the folder is searched without the index
)

type textFile struct {
	size, mod int64
	grams     []uint32 // sorted, unique
	skip      bool     // not indexed (too large, unreadable, binary): always a candidate
}

type textIndex struct {
	mu       sync.Mutex
	files    map[string]*textFile
	ids      []string
	inv      map[uint32][]int32
	built    bool
	pending  map[string]bool // changed since the inverted index was built
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

// syncText brings the per-file trigram sets up to date with the files on disk (reading only what changed).
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
	// Read what changed, on every core.
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

func (t *textIndex) rebuildLocked() {
	ids := make([]string, 0, len(t.files))
	for p := range t.files {
		ids = append(ids, p)
	}
	sort.Strings(ids)
	total := 0
	for _, p := range ids {
		total += len(t.files[p].grams)
	}
	t.postings = total
	if total > maxPostings {
		t.disabled, t.built, t.inv, t.ids = true, false, nil, nil
		return
	}
	inv := make(map[uint32][]int32, 1<<16)
	for id, p := range ids {
		for _, g := range t.files[p].grams {
			inv[g] = append(inv[g], int32(id))
		}
	}
	t.ids, t.inv, t.built, t.disabled = ids, inv, true, false
	t.pending = map[string]bool{}
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

// The text index on disk: per file its size, time and trigrams (delta-encoded varints, about a byte and a
// half each), so a restart reads only the files that changed while Falcon was stopped.
type savedText struct {
	Version int
	Files   map[string]savedTextFile
}
type savedTextFile struct {
	Size, Mod int64
	Skip      bool
	Grams     []byte
}

const savedTextVersion = 1

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
	if gob.NewDecoder(bufio.NewReader(f)).Decode(&sv) != nil || sv.Version != savedTextVersion {
		return
	}
	files := make(map[string]*textFile, len(sv.Files))
	for p, tf := range sv.Files {
		files[p] = &textFile{size: tf.Size, mod: tf.Mod, skip: tf.Skip, grams: unpack(tf.Grams)}
	}
	t.mu.Lock()
	if len(t.files) == 0 {
		t.files = files
		t.pending = map[string]bool{}
		t.rebuildLocked()
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
	sv := savedText{Version: savedTextVersion, Files: make(map[string]savedTextFile, len(t.files))}
	for p, tf := range t.files {
		sv.Files[p] = savedTextFile{Size: tf.size, Mod: tf.mod, Skip: tf.skip, Grams: pack(tf.grams)}
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

// pack writes sorted trigrams as varint gaps; unpack reads them back.
func pack(grams []uint32) []byte {
	out := make([]byte, 0, len(grams)*2)
	prev := uint32(0)
	for _, g := range grams {
		out = binary.AppendUvarint(out, uint64(g-prev))
		prev = g
	}
	return out
}

func unpack(b []byte) []uint32 {
	out := make([]uint32, 0, len(b)/2)
	prev := uint32(0)
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return out
		}
		prev += uint32(v)
		out = append(out, prev)
		b = b[n:]
	}
	return out
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
	// Checked before every answer: a file another tool wrote a moment ago is never missed. The check is
	// the listing plus one stat per file; only files that changed are read.
	stale := true
	t.mu.Unlock()
	if !ready || len(q) < 3 {
		if !ready {
			x.BuildText()
		}
		return nil, false
	}
	if stale {
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
	if !t.built {
		return nil, false
	}
	lists := make([][]int32, 0, len(grams))
	for _, g := range grams {
		lists = append(lists, t.inv[g])
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	var ids []int32
	if len(lists) > 0 {
		ids = append([]int32(nil), lists[0]...)
		for _, l := range lists[1:] {
			if len(ids) == 0 {
				break
			}
			ids = intersect(ids, l)
		}
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
	for _, p := range t.ids {
		if tf := t.files[p]; tf != nil && tf.skip && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, true
}

func intersect(a, b []int32) []int32 {
	out := a[:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
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
