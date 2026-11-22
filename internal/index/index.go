// Package index keeps, per folder, what navigation asks for again and again: the list of files (for the
// explorer filter, go to file and fuzzy matching) and every declared name (for go to definition and go to
// symbol). Both answer from memory; both stay current without being rebuilt.
//
//   - Files: the listing, served from memory.
//   - Symbols: every code file's declarations, keyed by name, saved to disk so a restart starts warm.
//   - Text: which files contain which trigrams, so a search reads only the files that can match.
//
// The folder's watcher (live.go) keeps all three current: when the system reports a change, only the
// files concerned are read again, and the listing only when files appeared or went. Requests never
// list or stat the folder. Where the tree cannot be watched, the listing is refreshed when it is a
// moment old and a search checks file times first.
package index

import (
	"context"
	"encoding/gob"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrohitv/falcon/internal/ide"
	"github.com/vrohitv/falcon/internal/outline"
	"github.com/vrohitv/falcon/internal/repository"
)

// FileTTL is how long a listing is served without checking again.
const FileTTL = 2 * time.Second

// Symbol is one declaration.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Sym is one declaration inside a file (the file is known from where it is kept).
type Sym struct {
	Name string
	Line int32
	Kind uint8
}

type fileSyms struct {
	Size int64
	Mod  int64
	Syms []Sym
}

// kinds are the declaration kinds, kept as small numbers.
var kinds = []string{"", "func", "method", "type", "struct", "interface", "var", "const", "class", "function", "def", "enum", "fn", "trait", "impl", "mod", "record", "union", "namespace"}

func kindID(k string) uint8 {
	for i, v := range kinds {
		if v == k {
			return uint8(i)
		}
	}
	return 0
}

// loc points at one declaration: a file (by number) and its place in that file's list.
type loc struct{ file, sym int32 }

// Index is one folder's index.
type Index struct {
	repo *repository.Repository
	save string

	mu      sync.Mutex
	files   []repository.File
	entries []ide.FileEntry
	listed  time.Time
	listing bool
	dirty   bool
	gen     uint64

	smu     sync.RWMutex
	perFile map[string]fileSyms
	// The lookup tables, rebuilt from perFile: every name once (sorted), where its declarations start in
	// locs, and the files by number. Flat arrays: a large folder costs tens of megabytes, not hundreds.
	names     []string
	lower     []string // the lowercase name, or "" when it is already lowercase
	start     []int32
	locs      []loc
	paths     []string
	pathSyms  [][]Sym
	built     time.Time
	building  bool
	ready     chan struct{}
	readyOnce sync.Once

	text textIndex

	lv  live
	wmu sync.Mutex                 // held by whatever changes the index (see live.go)
	set map[string]repository.File // the listing, by path (under mu)

	// closed: the folder was removed; nothing more is saved.
	closed atomic.Bool
}

// Close stops the index saving anything (its folder is no longer served).
func (x *Index) Close() {
	x.closed.Store(true)
	x.stopLive()
}

// New makes an index for a repository; save is where the symbol index is kept between runs ("" for none).
func New(repo *repository.Repository, save string) *Index {
	x := &Index{repo: repo, save: save, perFile: map[string]fileSyms{}, ready: make(chan struct{})}
	x.load()
	return x
}

// Invalidate marks the listing and symbols as possibly out of date (a save, a git change, an event).
// With the watcher running, only the generation moves: the watcher reports what changed on disk.
func (x *Index) Invalidate() {
	live := x.Live()
	x.mu.Lock()
	if !live {
		x.dirty = true
	}
	x.gen++
	x.mu.Unlock()
}

// Gen goes up whenever the folder may have changed; anything cached against an older value is stale.
func (x *Index) Gen() uint64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.gen
}

// Files is the folder's file listing. The first call waits for git; later calls return at once and refresh
// in the background when the listing is old or marked out of date.
func (x *Index) Files(ctx context.Context) ([]repository.File, error) {
	x.Watch()
	live := x.Live()
	x.mu.Lock()
	if x.files == nil {
		x.mu.Unlock()
		return x.list(ctx)
	}
	files := x.files
	if (x.dirty || (!live && time.Since(x.listed) > FileTTL)) && !x.listing {
		x.listing = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = x.list(ctx)
		}()
	}
	x.mu.Unlock()
	return files, nil
}

// Fresh is the listing, waiting for a refresh if it is marked out of date (after Falcon itself changed a file).
func (x *Index) Fresh(ctx context.Context) ([]repository.File, error) {
	x.mu.Lock()
	dirty := x.dirty || x.files == nil
	x.mu.Unlock()
	if dirty {
		return x.list(ctx)
	}
	return x.Files(ctx)
}

func (x *Index) list(ctx context.Context) ([]repository.File, error) {
	x.mu.Lock()
	x.dirty = false
	x.mu.Unlock()
	files, err := x.repo.Files(ctx)
	x.mu.Lock()
	defer x.mu.Unlock()
	x.listing = false
	if err != nil {
		return nil, err
	}
	x.setListingLocked(files)
	return files, nil
}

func (x *Index) setListingLocked(files []repository.File) {
	paths := make([]string, len(files))
	set := make(map[string]repository.File, len(files))
	for i, f := range files {
		paths[i] = f.Path
		set[f.Path] = f
	}
	x.files, x.entries, x.listed, x.set = files, ide.Entries(paths), time.Now(), set
}

// setListing replaces the listing with one kept by path (the watcher's changes). When only sizes
// changed (files saved, none added or removed), the paths' match entries are kept.
func (x *Index) setListing(set map[string]repository.File, pathsChanged bool) {
	files := make([]repository.File, 0, len(set))
	for _, f := range set {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	x.mu.Lock()
	defer x.mu.Unlock()
	if pathsChanged || x.entries == nil {
		x.setListingLocked(files)
		return
	}
	x.files, x.listed, x.set = files, time.Now(), set
}

// Fuzzy matches a query against every file path.
func (x *Index) Fuzzy(ctx context.Context, q string, limit int) ([]ide.FuzzyResult, error) {
	if _, err := x.Files(ctx); err != nil {
		return nil, err
	}
	x.mu.Lock()
	entries := x.entries
	x.mu.Unlock()
	return ide.FuzzyFind(entries, q, limit), nil
}

// code are the files whose declarations are indexed.
var code = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".svelte": true, ".vue": true, ".py": true, ".pyi": true, ".rs": true,
	".rb": true, ".java": true, ".kt": true, ".swift": true, ".c": true, ".h": true, ".cpp": true,
	".cc": true, ".cxx": true, ".hpp": true, ".hh": true, ".cs": true, ".php": true, ".scala": true,
	".dart": true, ".lua": true, ".zig": true,
}

const maxIndexed = 1 << 20

// Refresh brings the symbol index up to date: new and changed files are parsed, removed ones dropped.
// Only one refresh runs at a time; the others return at once.
func (x *Index) Refresh(ctx context.Context) {
	x.Watch()
	x.wmu.Lock()
	defer x.wmu.Unlock()
	x.refreshLocked(ctx, nil)
}

// refreshLocked refreshes every file (only == nil), or just the files named (a change the watcher
// reported). The caller holds wmu.
func (x *Index) refreshLocked(ctx context.Context, only []string) {
	x.smu.Lock()
	if x.building || (only != nil && x.built.IsZero() && len(x.names) == 0) {
		x.smu.Unlock()
		return // a first build reads everything anyway
	}
	x.building = true
	old := x.perFile
	x.smu.Unlock()
	defer func() {
		x.smu.Lock()
		x.building = false
		x.smu.Unlock()
	}()
	var files []repository.File
	var next map[string]fileSyms
	removedSome := false
	if only == nil {
		var err error
		if files, err = x.Fresh(ctx); err != nil {
			return
		}
		next = make(map[string]fileSyms, len(files))
	} else {
		x.mu.Lock()
		set := x.set
		x.mu.Unlock()
		next = make(map[string]fileSyms, len(old))
		for p, fs := range old {
			next[p] = fs
		}
		for _, p := range only {
			if _, listed := set[p]; listed {
				if info, err := os.Lstat(filepath.Join(x.repo.Path, p)); err == nil && info.Mode().IsRegular() {
					files = append(files, repository.File{Path: p, Size: info.Size()})
					continue
				}
			}
			if _, had := next[p]; had {
				delete(next, p)
				removedSome = true
			}
		}
		for _, f := range files {
			delete(next, f.Path) // decided again below
		}
	}
	changed := 0
	type job struct {
		path      string
		size, mod int64
	}
	work := []job{}
	for _, f := range files {
		if !code[strings.ToLower(filepath.Ext(f.Path))] || f.Size > maxIndexed {
			continue
		}
		info, err := os.Stat(filepath.Join(x.repo.Path, f.Path))
		if err != nil {
			continue
		}
		mod := info.ModTime().UnixNano()
		if prev, ok := old[f.Path]; ok && prev.Size == info.Size() && prev.Mod == mod {
			next[f.Path] = prev
			continue
		}
		work = append(work, job{f.Path, info.Size(), mod})
	}
	// Parse what changed on every core.
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan job)
	for w := 0; w < workers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				raw, err := x.repo.ReadText(j.path)
				if err != nil {
					continue
				}
				defs := outline.Of(j.path, string(raw))
				syms := make([]Sym, len(defs))
				for i, d := range defs {
					syms[i] = Sym{Name: d.Name, Line: int32(d.Line), Kind: kindID(d.Kind)}
				}
				mu.Lock()
				next[j.path] = fileSyms{Size: j.size, Mod: j.mod, Syms: syms}
				changed++
				mu.Unlock()
			}
		}()
	}
	for _, j := range work {
		select {
		case jobs <- j:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	removed := removedSome
	for p := range old {
		if removed {
			break
		}
		if _, ok := next[p]; !ok {
			removed = true
		}
	}
	if changed == 0 && !removed && !x.built.IsZero() {
		x.smu.Lock()
		x.built = time.Now()
		x.smu.Unlock()
		return
	}
	t := tables(next)
	x.smu.Lock()
	x.perFile, x.built = next, time.Now()
	x.setTables(t)
	x.smu.Unlock()
	x.readyOnce.Do(func() { close(x.ready) })
	x.persist()
}

func workers() int {
	n := 4
	if c := runtime.NumCPU(); c > n {
		n = c
	}
	return n
}

// Ready reports whether the symbol index has been built (or loaded) at least once.
func (x *Index) Ready() bool {
	x.smu.RLock()
	defer x.smu.RUnlock()
	return !x.built.IsZero() || len(x.names) > 0
}

// Warm starts a refresh in the background when the index is older than ttl.
func (x *Index) Warm(ttl time.Duration) {
	if x.Live() && x.Ready() {
		return // the watcher keeps it current
	}
	x.smu.RLock()
	stale := time.Since(x.built) > ttl && !x.building
	x.smu.RUnlock()
	if stale {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			x.Refresh(ctx)
		}()
	}
}

// Lookup is every declaration of name.
func (x *Index) Lookup(name string) []Symbol {
	x.smu.RLock()
	defer x.smu.RUnlock()
	i := sort.SearchStrings(x.names, name)
	if i == len(x.names) || x.names[i] != name {
		return nil
	}
	out := []Symbol{}
	for _, l := range x.locs[x.start[i]:x.start[i+1]] {
		out = append(out, x.symbol(l))
	}
	return out
}

// Search finds declarations whose name matches q: exact first, then prefix, then word starts
// (CamelCase or snake_case), then anywhere; case-insensitive unless q has capitals.
func (x *Index) Search(q string, limit int) []Symbol {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Symbol{}
	}
	sensitive := strings.ToLower(q) != q
	lq := strings.ToLower(q)
	x.smu.RLock()
	defer x.smu.RUnlock()
	type scored struct {
		name  string
		score int
	}
	hits := []scored{}
	for i, n := range x.names {
		hay, needle := x.lower[i], lq
		if hay == "" {
			hay = n
		}
		if sensitive {
			hay, needle = n, q
		}
		at := strings.Index(hay, needle)
		if at < 0 {
			continue
		}
		score := 3
		switch {
		case len(hay) == len(needle):
			score = 0
		case at == 0:
			score = 1
		case boundary(n, at):
			score = 2
		}
		hits = append(hits, scored{n, score*1000 + len(n)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	out := []Symbol{}
	for _, h := range hits {
		i := sort.SearchStrings(x.names, h.name)
		for _, l := range x.locs[x.start[i]:x.start[i+1]] {
			s := x.symbol(l)
			if s.Name != h.name && strings.Contains(s.Name, ".") {
				continue // a method is listed once, under its full name
			}
			out = append(out, s)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func boundary(name string, at int) bool {
	if at <= 0 || at >= len(name) {
		return at == 0
	}
	prev, cur := name[at-1], name[at]
	return prev == '_' || prev == '.' || (prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z')
}

type saved struct {
	Version int
	Files   map[string]fileSyms
}

const savedVersion = 2

func (x *Index) load() {
	if x.save == "" {
		return
	}
	f, err := os.Open(x.save)
	if err != nil {
		return
	}
	defer f.Close()
	var s saved
	if gob.NewDecoder(f).Decode(&s) != nil || s.Version != savedVersion {
		return
	}
	x.perFile = s.Files
	x.setTables(tables(s.Files))
}

func (x *Index) persist() {
	if x.save == "" || x.closed.Load() {
		return
	}
	x.smu.RLock()
	s := saved{Version: savedVersion, Files: x.perFile}
	x.smu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(x.save), 0o700); err != nil {
		return
	}
	tmp := x.save + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	if gob.NewEncoder(f).Encode(s) != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	_ = os.Rename(tmp, x.save)
}

type symTables struct {
	names, lower []string
	start        []int32
	locs         []loc
	paths        []string
	pathSyms     [][]Sym
}

// tables builds the lookup arrays: each declaration under its name, and a method (T.M) under M too.
func tables(files map[string]fileSyms) symTables {
	var t symTables
	t.paths = make([]string, 0, len(files))
	for p := range files {
		t.paths = append(t.paths, p)
	}
	sort.Strings(t.paths)
	t.pathSyms = make([][]Sym, len(t.paths))
	type entry struct {
		name string
		at   loc
	}
	n := 0
	for _, p := range t.paths {
		n += len(files[p].Syms)
	}
	entries := make([]entry, 0, n+n/4)
	for f, p := range t.paths {
		syms := files[p].Syms
		t.pathSyms[f] = syms
		for i, sy := range syms {
			entries = append(entries, entry{sy.Name, loc{int32(f), int32(i)}})
			if j := strings.LastIndexByte(sy.Name, '.'); j > 0 {
				entries = append(entries, entry{sy.Name[j+1:], loc{int32(f), int32(i)}})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		if entries[i].at.file != entries[j].at.file {
			return entries[i].at.file < entries[j].at.file
		}
		return entries[i].at.sym < entries[j].at.sym
	})
	t.locs = make([]loc, len(entries))
	for i, e := range entries {
		t.locs[i] = e.at
		if i == 0 || e.name != entries[i-1].name {
			t.names = append(t.names, e.name)
			t.start = append(t.start, int32(i))
		}
	}
	t.start = append(t.start, int32(len(entries)))
	t.lower = make([]string, len(t.names))
	for i, name := range t.names {
		if l := strings.ToLower(name); l != name {
			t.lower[i] = l
		}
	}
	return t
}

func (x *Index) setTables(t symTables) {
	x.names, x.lower, x.start, x.locs, x.paths, x.pathSyms = t.names, t.lower, t.start, t.locs, t.paths, t.pathSyms
}

func (x *Index) symbol(l loc) Symbol {
	sy := x.pathSyms[l.file][l.sym]
	return Symbol{Name: sy.Name, Kind: kinds[sy.Kind], Path: x.paths[l.file], Line: int(sy.Line)}
}
