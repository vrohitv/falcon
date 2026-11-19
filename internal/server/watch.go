package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/workspace"
)

const (
	maxWatched   = 16
	watchMaxRead = 2 << 20
	// Atomic saves produce a burst (create, write, rename); one short,
	// event-triggered delay coalesces it into a single notification.
	settle = 40 * time.Millisecond
)

type fileEvent struct {
	Path   string `json:"path"`
	Hash   string `json:"hash"`
	Exists bool   `json:"exists"`
}

type watchSub struct {
	files map[string]string // absolute path -> repository path
	ch    chan fileEvent
}

// fileWatcher pushes changes to files open in the IDE using OS notifications
// (inotify, kqueue). It watches parent directories, because atomic saves
// replace a file's inode, and only while someone is watching. When the OS
// cannot watch (limits, unsupported filesystems), subscribe fails and the
// client falls back to checking on focus; nothing ever polls.
type fileWatcher struct {
	mu      sync.Mutex
	w       *fsnotify.Watcher
	dirs    map[string]int
	subs    map[*watchSub]struct{}
	pending map[string]*time.Timer
}

func (f *fileWatcher) subscribe(files map[string]string) (*watchSub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.w == nil {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		f.w, f.dirs, f.subs, f.pending = w, map[string]int{}, map[*watchSub]struct{}{}, map[string]*time.Timer{}
		go f.loop(w)
	}
	added := []string{}
	for abs := range files {
		dir := filepath.Dir(abs)
		if f.dirs[dir] == 0 {
			if err := f.w.Add(dir); err != nil {
				for _, d := range added {
					f.release(d)
				}
				return nil, err
			}
		}
		f.dirs[dir]++
		added = append(added, dir)
	}
	sub := &watchSub{files: files, ch: make(chan fileEvent, 64)}
	f.subs[sub] = struct{}{}
	return sub, nil
}

func (f *fileWatcher) release(dir string) {
	f.dirs[dir]--
	if f.dirs[dir] <= 0 {
		delete(f.dirs, dir)
		_ = f.w.Remove(dir)
	}
}

func (f *fileWatcher) unsubscribe(sub *watchSub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs, sub)
	for abs := range sub.files {
		f.release(filepath.Dir(abs))
	}
}

func (f *fileWatcher) loop(w *fsnotify.Watcher) {
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// Reading a file reports an attribute change on macOS; only content changes matter.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			f.changed(ev.Name)
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
		}
	}
}

func (f *fileWatcher) changed(abs string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	watched := false
	for sub := range f.subs {
		_, ok := sub.files[abs]
		watched = watched || ok
	}
	if !watched {
		return
	}
	if t := f.pending[abs]; t != nil {
		t.Reset(settle)
		return
	}
	f.pending[abs] = time.AfterFunc(settle, func() { f.deliver(abs) })
}

func (f *fileWatcher) deliver(abs string) {
	hash, exists := currentHash(abs)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, abs)
	for sub := range f.subs {
		if rel, ok := sub.files[abs]; ok {
			select {
			case sub.ch <- fileEvent{Path: rel, Hash: hash, Exists: exists}:
			default:
			}
		}
	}
}

// currentHash matches the content hash the editor and viewer saw, so clients
// can ignore notifications for their own saves.
func currentHash(abs string) (string, bool) {
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	if info.Size() > watchMaxRead {
		return "", true
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", !errors.Is(err, os.ErrNotExist)
	}
	return domain.Hash(b), true
}

// watchFiles streams changes to the files open in the IDE as server-sent
// events. The first event per path reports its current hash, so a client
// that reconnects detects anything it missed.
func (s *Server) watchFiles(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	paths := r.URL.Query()["path"]
	if len(paths) == 0 || len(paths) > maxWatched {
		fail(w, fmt.Errorf("watch 1–%d paths", maxWatched))
		return
	}
	files := map[string]string{}
	for _, p := range paths {
		abs, err := s.readable(rp, p)
		if err != nil {
			fail(w, err)
			return
		}
		files[abs] = p
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	sub, err := s.watcher.subscribe(files)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write(domain.JSON(map[string]string{"error": "file watching is unavailable here: " + err.Error()}))
		return
	}
	defer s.watcher.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(e fileEvent) error {
		_, err := fmt.Fprintf(w, "event: file\ndata: %s\n\n", domain.JSON(e))
		return err
	}
	for abs, rel := range files {
		hash, exists := currentHash(abs)
		if send(fileEvent{Path: rel, Hash: hash, Exists: exists}) != nil {
			return
		}
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-sub.ch:
			if send(e) != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// fileStat is the fallback when watching is unavailable: the client asks for
// a file's current hash at discrete moments (focus, tab switch, editing).
func (s *Server) fileStat(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	p := r.URL.Query().Get("path")
	abs, err := s.readable(rp, p)
	if err != nil {
		fail(w, err)
		return
	}
	hash, exists := currentHash(abs)
	respond(w, fileEvent{Path: p, Hash: hash, Exists: exists})
}
