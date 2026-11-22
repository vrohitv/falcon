// Package treewatch reports changes anywhere in a folder's working tree, from the operating system's
// notifications: one recursive watch on Windows (ReadDirectoryChangesW over the whole tree), one inotify
// watch per directory on Linux. Nothing polls. The .git directory and directories git ignores are left
// out (node_modules, build output), so a build does not flood it.
//
// Changes arrive batched: a burst (a checkout, a formatter run, a build) is one Change, sent once the
// tree has been quiet for a moment.
package treewatch

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Change is a batch of changes.
type Change struct {
	// Paths changed (folder-relative, slash-separated): files written, created or removed, and
	// directories created, removed or renamed.
	Paths []string
	// Structure: something was created, removed or renamed, so the list of files may differ.
	Structure bool
	// Made: the paths among Paths that were created or renamed into place (a new directory's contents
	// are read whole; a directory merely written to is not).
	Made []string
	// All: notifications were lost (the system's queue overflowed); anything may have changed.
	All bool
}

// Ignore reports whether a folder-relative directory is left out (git ignores it).
type Ignore func(dir string) bool

// Watcher watches one folder.
type Watcher struct {
	// C delivers the batches; it is closed when the watcher stops.
	C <-chan Change

	root   string
	ignore Ignore
	stop   chan struct{}
	once   sync.Once
	done   sync.WaitGroup

	mu        sync.Mutex
	paths     map[string]struct{}
	made      map[string]struct{}
	structure bool
	all       bool
	kick      chan struct{}
}

const (
	quiet   = 40 * time.Millisecond  // a batch is sent once the tree has been quiet this long
	longest = 250 * time.Millisecond // or this long after its first change, whichever is first
)

// Start begins watching root. It fails when the system cannot watch the tree (inotify's limit reached,
// an unsupported filesystem); the caller then checks for changes itself.
func Start(root string, ignore Ignore) (*Watcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if ignore == nil {
		ignore = func(string) bool { return false }
	}
	out := make(chan Change)
	w := &Watcher{C: out, root: abs, ignore: ignore, stop: make(chan struct{}), paths: map[string]struct{}{}, made: map[string]struct{}{}, kick: make(chan struct{}, 1)}
	stopBackend, err := w.start()
	if err != nil {
		return nil, err
	}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		defer close(out)
		defer stopBackend()
		w.batch(out)
	}()
	return w, nil
}

// Close stops watching.
func (w *Watcher) Close() {
	w.once.Do(func() { close(w.stop) })
	w.done.Wait()
}

// skipped reports whether a folder-relative path is inside .git or an ignored directory.
func (w *Watcher) skipped(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == ".git" {
			return true
		}
		if i < len(parts)-1 && w.ignore(strings.Join(parts[:i+1], "/")) {
			return true
		}
	}
	return false
}

// rel is a path inside the folder, slash-separated ("" when it is outside).
func (w *Watcher) rel(abs string) string {
	r, err := filepath.Rel(w.root, abs)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return ""
	}
	return filepath.ToSlash(r)
}

// note records a change from a backend (made: created or renamed into place).
func (w *Watcher) note(rel string, structure, made bool) {
	if rel == "" || w.skipped(rel) {
		return
	}
	w.mu.Lock()
	w.paths[rel] = struct{}{}
	if made {
		w.made[rel] = struct{}{}
	}
	w.structure = w.structure || structure
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// lost records that notifications were lost.
func (w *Watcher) lost() {
	w.mu.Lock()
	w.all = true
	w.mu.Unlock()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (w *Watcher) batch(out chan<- Change) {
	var first time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-w.kick:
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			wait := min(quiet, longest-now.Sub(first))
			timer.Reset(max(wait, 0))
			continue
		case <-timer.C:
		}
		w.mu.Lock()
		c := Change{Paths: make([]string, 0, len(w.paths)), Structure: w.structure, All: w.all}
		for p := range w.paths {
			c.Paths = append(c.Paths, p)
		}
		for p := range w.made {
			c.Made = append(c.Made, p)
		}
		w.paths, w.made, w.structure, w.all = map[string]struct{}{}, map[string]struct{}{}, false, false
		w.mu.Unlock()
		first = time.Time{}
		if len(c.Paths) == 0 && !c.All {
			continue
		}
		// Changes noted while the batch waits to be taken go into the next one.
		select {
		case out <- c:
		case <-w.stop:
			return
		}
	}
}
