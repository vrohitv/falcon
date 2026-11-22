package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/treewatch"
)

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
	ignored map[string]bool // directories git ignores (known from the start, or asked as they appear)
	asking  bool            // the start is over: an unknown directory is asked about
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

// isIgnored tells the watcher which directories to leave out: those git ignores. The ones present at the
// start come from one git call; a directory made later is asked about once.
func (x *Index) isIgnored(dir string) bool {
	l := &x.lv
	l.mu.Lock()
	v, known := l.ignored[dir]
	asking := l.asking
	l.mu.Unlock()
	if known || !asking {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := x.repo.Git(ctx, "check-ignore", "-q", "--", dir+"/")
	l.mu.Lock()
	l.ignored[dir] = err == nil
	l.mu.Unlock()
	return err == nil
}

func (x *Index) runLive() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	out, err := x.repo.Git(ctx, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	cancel()
	ignored := map[string]bool{}
	if err == nil {
		for _, p := range strings.Split(out, "\x00") {
			if strings.HasSuffix(p, "/") {
				ignored[strings.TrimSuffix(p, "/")] = true
			}
		}
	}
	l := &x.lv
	l.mu.Lock()
	l.ignored, l.asking = ignored, false
	l.mu.Unlock()
	w, err := treewatch.Start(x.repo.Path, x.isIgnored)
	l.mu.Lock()
	l.asking = true
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
	x.mu.Lock()
	structure := false
	for _, p := range paths {
		if _, ok := x.set[p]; !ok {
			structure = true
		}
	}
	x.mu.Unlock()
	// A save does not wait behind a long build: the watcher reports the same change a moment later.
	if !x.wmu.TryLock() {
		x.Invalidate()
		return
	}
	defer x.wmu.Unlock()
	x.applyLocked(treewatch.Change{Paths: paths, Structure: structure})
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
	x.mu.Lock()
	x.gen++
	before := x.set
	x.mu.Unlock()
	if c.All {
		x.mu.Lock()
		x.dirty = true
		x.mu.Unlock()
		if files, err := x.Fresh(ctx); err == nil {
			paths := make([]string, len(files))
			for i, f := range files {
				paths[i] = f.Path
			}
			x.text.mu.Lock()
			built := x.text.files != nil
			x.text.mu.Unlock()
			if built {
				x.syncText(ctx, paths, true)
				x.saveText()
			}
			if x.Ready() {
				x.refreshLocked(ctx, nil)
			}
		}
		x.notify()
		return
	}
	changed := map[string]bool{}
	dirs := []string{}
	gitignore := false
	for _, p := range c.Paths {
		changed[p] = true
		if filepath.Base(p) == ".gitignore" {
			gitignore = true
		}
	}
	if c.Structure {
		if _, err := x.list(ctx); err == nil {
			x.mu.Lock()
			after := x.set
			x.mu.Unlock()
			// Files that came or went (a directory renamed reports only the directory).
			for p := range before {
				if _, ok := after[p]; !ok {
					changed[p] = true
				}
			}
			for p := range after {
				if _, ok := before[p]; !ok {
					changed[p] = true
				}
			}
		}
	}
	// A directory reported (made, or written to) stands for the listed files in it.
	x.mu.Lock()
	set := x.set
	for p := range changed {
		if _, ok := set[p]; !ok {
			if _, was := before[p]; !was {
				dirs = append(dirs, p+"/")
			}
		}
	}
	if len(dirs) > 0 {
		for p := range set {
			for _, d := range dirs {
				if strings.HasPrefix(p, d) {
					changed[p] = true
					break
				}
			}
		}
	}
	x.mu.Unlock()
	paths := make([]string, 0, len(changed))
	for p := range changed {
		paths = append(paths, p)
	}
	x.syncTextPaths(paths, set)
	x.refreshLocked(ctx, paths)
	x.notify()
	if gitignore {
		// What is ignored changed: watch again with the new answer (and take everything in once).
		go func() {
			x.stopWatcher()
			x.lv.mu.Lock()
			x.lv.started = false
			x.lv.mu.Unlock()
			x.Watch()
		}()
	}
}

// syncTextPaths brings the text of just these files up to date. Many changes are folded into the
// inverted index at once; a few are folded in after a quiet moment (until then each is decided by its
// own trigrams).
func (x *Index) syncTextPaths(paths []string, listed map[string]struct{}) {
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
