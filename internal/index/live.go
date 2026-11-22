package index

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/repository"
	"github.com/vrohitv/falcon/internal/treewatch"
)

// FileEvent is one file that came, changed or went (for the language servers).
type FileEvent struct {
	Path string
	Kind int // Created, Changed, Deleted (the numbers LSP uses)
}

const (
	Created = 1
	Changed = 2
	Deleted = 3
)

// OnFiles calls f with the files each batch created, changed or deleted; the returned function stops it.
func (x *Index) OnFiles(f func([]FileEvent)) func() {
	l := &x.lv
	l.mu.Lock()
	if l.files == nil {
		l.files = map[int]func([]FileEvent){}
	}
	l.nextSub++
	id := l.nextSub
	l.files[id] = f
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		delete(l.files, id)
		l.mu.Unlock()
	}
}

func (x *Index) tellFiles(events []FileEvent) {
	if len(events) == 0 {
		return
	}
	x.lv.mu.Lock()
	fs := make([]func([]FileEvent), 0, len(x.lv.files))
	for _, f := range x.lv.files {
		fs = append(fs, f)
	}
	x.lv.mu.Unlock()
	for _, f := range fs {
		f(events)
	}
}

// The folder's watcher is the source of truth. Once it runs, the listing, the text index and the symbols
// change when the operating system says a file changed, and only for what changed; a search or a
// lookup reads memory and never lists or stats the folder. (Without a watcher, when the system cannot
// watch the tree, each search checks the listing and file times itself, as before.)
//
// Every change to the index's state goes through one writer lock (wmu): the watcher's batches, the first
// full build, a rebuild, Falcon's own saves. A change that arrives during a slow first build waits in
// the watcher's batch and is applied after it, so nothing falls between.
type live struct {
	mu      sync.Mutex
	started bool
	w       *treewatch.Watcher
	ready   bool  // watching, and everything before the watch began has been taken in
	err     error // why the folder cannot be watched
	subs    map[chan struct{}]struct{}
	files   map[int]func([]FileEvent)
	nextSub int
	rebuild *time.Timer
}

// noWatch turns the watcher off (tests of the checking fallback).
var noWatch bool

// Watch starts the folder's watcher (once; in the background).
func (x *Index) Watch() {
	l := &x.lv
	l.mu.Lock()
	if l.started || x.closed.Load() || noWatch {
		l.mu.Unlock()
		return
	}
	l.started = true
	l.mu.Unlock()
	go x.runLive()
}

// Live reports whether the watcher keeps the index current.
func (x *Index) Live() bool {
	x.lv.mu.Lock()
	defer x.lv.mu.Unlock()
	return x.lv.ready
}

// Changes is told after each change from the watcher has been applied; it is closed when the index
// closes. The returned function stops it.
func (x *Index) Changes() (<-chan struct{}, func()) {
	l := &x.lv
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	if x.closed.Load() {
		l.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	if l.subs == nil {
		l.subs = map[chan struct{}]struct{}{}
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
		l.mu.Unlock()
	}
}

func (x *Index) notify() {
	l := &x.lv
	l.mu.Lock()
	for ch := range l.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	l.mu.Unlock()
}

// stopLive ends the watcher and its subscriptions (the index closes).
func (x *Index) stopLive() {
	x.lv.mu.Lock()
	for ch := range x.lv.subs {
		close(ch)
	}
	x.lv.subs = nil
	x.lv.mu.Unlock()
	x.stopWatcher()
}

// stopWatcher ends the watcher; subscribers stay (it may start again).
func (x *Index) stopWatcher() {
	l := &x.lv
	l.mu.Lock()
	w := l.w
	l.w, l.ready = nil, false
	if l.rebuild != nil {
		l.rebuild.Stop()
	}
	l.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

// skipDir tells the watcher which directories to leave out: those git ignores (answered in memory, see
// repository.Ignorer) and repositories inside the folder.
func (x *Index) skipDir(dir string) bool {
	if x.repo.Ignorer().SkipDir(dir) {
		return true
	}
	_, err := os.Lstat(filepath.Join(x.repo.Path, filepath.FromSlash(dir), ".git"))
	return err == nil
}

func (x *Index) runLive() {
	l := &x.lv
	x.repo.Ignorer().Sync()
	w, err := treewatch.Start(x.repo.Path, x.skipDir)
	l.mu.Lock()
	if err != nil {
		l.err = err
		l.started = false // tried again on a later use
		l.mu.Unlock()
		return
	}
	if x.closed.Load() {
		l.mu.Unlock()
		w.Close()
		return
	}
	l.w = w
	l.mu.Unlock()

	// What changed before the watch began is taken in once, as a check would; from now on only what the
	// watcher reports is looked at.
	x.wmu.Lock()
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Minute)
	x.mu.Lock()
	x.dirty = true
	x.mu.Unlock()
	if files, err := x.Fresh(rctx); err == nil {
		x.text.mu.Lock()
		built := x.text.files != nil
		x.text.mu.Unlock()
		if built {
			paths := make([]string, len(files))
			for i, f := range files {
				paths[i] = f.Path
			}
			x.syncText(rctx, paths, true)
		}
		if x.Ready() {
			x.refreshLocked(rctx, nil)
		}
	}
	rcancel()
	l.mu.Lock()
	l.ready = l.w == w
	l.mu.Unlock()
	x.wmu.Unlock()

	for c := range w.C {
		x.apply(c)
	}
}

// Changed applies Falcon's own change to files at once (a save), without waiting for the watcher.
func (x *Index) Changed(paths ...string) {
	if !x.Live() {
		x.Invalidate()
		return
	}
	// A save does not wait behind a long build: the watcher reports the same change a moment later.
	if !x.wmu.TryLock() {
		x.Invalidate()
		return
	}
	defer x.wmu.Unlock()
	x.applyLocked(treewatch.Change{Paths: paths, Structure: true})
}

// apply takes in one batch of changes: the listing again only when files may have appeared or gone, and
// the text and symbols of just the files concerned.
func (x *Index) apply(c treewatch.Change) {
	x.wmu.Lock()
	defer x.wmu.Unlock()
	x.applyLocked(c)
}

func (x *Index) applyLocked(c treewatch.Change) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ig := x.repo.Ignorer()
	ig.Sync() // git's index may have changed (a file tracked or untracked)
	gitignore := false
	for _, p := range c.Paths {
		if path.Base(p) == ".gitignore" {
			gitignore = true
		}
	}
	if gitignore {
		ig.Reload()
	}
	x.mu.Lock()
	x.gen++
	before := x.set
	x.mu.Unlock()
	after := before
	if c.All || gitignore || before == nil {
		// Anything may have changed: the folder is walked again.
		if _, err := x.list(ctx); err == nil {
			x.mu.Lock()
			after = x.set
			x.mu.Unlock()
		}
	} else {
		after = x.updateListing(ctx, c)
	}
	// What came, went or was written, for the text, the symbols and the language servers.
	events := []FileEvent{}
	paths := []string{}
	for p := range after {
		if _, ok := before[p]; !ok {
			events = append(events, FileEvent{Path: p, Kind: Created})
			paths = append(paths, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			events = append(events, FileEvent{Path: p, Kind: Deleted})
			paths = append(paths, p)
		}
	}
	if c.All || gitignore {
		// Every file's time is compared (only the changed ones are read).
		for p := range after {
			if _, ok := before[p]; ok {
				paths = append(paths, p)
			}
		}
	} else {
		for _, p := range c.Paths {
			_, was := before[p]
			_, is := after[p]
			if was && is {
				events = append(events, FileEvent{Path: p, Kind: Changed})
				paths = append(paths, p)
			}
		}
	}
	x.syncTextPaths(paths, after)
	x.refreshLocked(ctx, paths)
	x.notify()
	x.tellFiles(events)
	if gitignore {
		// What is ignored changed: directories may need watching (or no longer): watch again.
		go func() {
			x.stopWatcher()
			x.lv.mu.Lock()
			x.lv.started = false
			x.lv.mu.Unlock()
			x.Watch()
		}()
	}
}

// updateListing applies a batch to the listing without walking the folder: each path reported is looked
// at (a file is listed or not by the same rules as a walk), a directory that went takes its files with
// it, and only a directory made just now is walked. It returns the new listing.
func (x *Index) updateListing(ctx context.Context, c treewatch.Change) map[string]repository.File {
	x.mu.Lock()
	cur := x.set
	x.mu.Unlock()
	made := map[string]bool{}
	for _, p := range c.Made {
		made[p] = true
	}
	next := make(map[string]repository.File, len(cur))
	for p, f := range cur {
		next[p] = f
	}
	gone := []string{}
	changed := false
	for _, p := range c.Paths {
		info, err := os.Lstat(filepath.Join(x.repo.Path, filepath.FromSlash(p)))
		switch {
		case err != nil:
			if _, ok := next[p]; ok {
				delete(next, p)
				changed = true
			}
			gone = append(gone, p+"/")
		case info.IsDir():
			if made[p] && !x.skipDir(p) {
				if fs, err := x.repo.FilesUnder(ctx, p); err == nil {
					for _, f := range fs {
						next[f.Path] = f
					}
					changed = true
				}
			}
		default:
			f, ok := x.repo.FileAt(p)
			if prev, had := next[p]; ok && (!had || prev != f) {
				next[p] = f
				changed = true
			} else if !ok && had {
				delete(next, p)
				changed = true
			}
		}
	}
	if len(gone) > 0 {
		for p := range next {
			for _, g := range gone {
				if strings.HasPrefix(p, g) {
					delete(next, p)
					changed = true
					break
				}
			}
		}
	}
	if !changed {
		return cur
	}
	x.setListing(next)
	return next
}

// syncTextPaths brings the text of just these files up to date. Many changes are folded into the
// inverted index at once; a few are folded in after a quiet moment (until then each is decided by its
// own trigrams).
func (x *Index) syncTextPaths(paths []string, listed map[string]repository.File) {
	t := &x.text
	t.mu.Lock()
	if t.files == nil {
		t.mu.Unlock()
		return // not built yet: its first build reads everything
	}
	type stat struct {
		path      string
		size, mod int64
	}
	work := []stat{}
	for _, p := range paths {
		if _, ok := listed[p]; !ok {
			if _, had := t.files[p]; had {
				delete(t.files, p)
				t.pending[p] = true
			}
			continue
		}
		info, err := os.Lstat(filepath.Join(x.repo.Path, p))
		if err != nil {
			if _, had := t.files[p]; had {
				delete(t.files, p)
				t.pending[p] = true
			}
			continue
		}
		if prev, ok := t.files[p]; ok && prev.size == info.Size() && prev.mod == info.ModTime().UnixNano() {
			continue
		}
		work = append(work, stat{p, info.Size(), info.ModTime().UnixNano()})
	}
	t.mu.Unlock()
	read := make(map[string]*textFile, len(work))
	for _, s := range work {
		read[s.path] = x.readText(s.path)
	}
	t.mu.Lock()
	for p, tf := range read {
		t.files[p] = tf
		t.pending[p] = true
	}
	many := len(t.pending) > 256
	some := len(t.pending) > 0
	if many {
		t.rebuildLocked()
	}
	t.mu.Unlock()
	if many {
		x.saveText()
	} else if some {
		x.foldLater()
	}
}

// foldLater folds the changed files into the inverted index once the folder has been quiet for a while.
func (x *Index) foldLater() {
	l := &x.lv
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rebuild != nil {
		l.rebuild.Stop()
	}
	l.rebuild = time.AfterFunc(20*time.Second, func() {
		if x.closed.Load() {
			return
		}
		x.wmu.Lock()
		x.text.mu.Lock()
		some := len(x.text.pending) > 0 && x.text.built
		if some {
			x.text.rebuildLocked()
		}
		x.text.mu.Unlock()
		x.wmu.Unlock()
		if some {
			x.saveText()
		}
	})
}
