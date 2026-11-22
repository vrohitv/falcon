package server

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vrohitv/falcon/internal/index"
	"github.com/vrohitv/falcon/internal/lsp"
	"github.com/vrohitv/falcon/internal/workspace"
)

// serverSettings are what a server is told about the folder at start and asks for later
// (workspace/configuration). rust-analyzer discovers Cargo projects only one level below its root, so it
// is given every Cargo workspace in the folder and every crate that is in none, by path (the documented
// linkedProjects setting); the others find their projects themselves.
func (s *Server) serverSettings(rp *workspace.Repo, server string) map[string]any {
	if server != "rust-analyzer" {
		return nil
	}
	files, err := s.index(rp).Files(context.Background())
	if err != nil {
		return nil
	}
	var manifests []string
	for _, f := range files {
		if path.Base(f.Path) == "Cargo.toml" {
			manifests = append(manifests, f.Path)
		}
	}
	var workspaces, linked []string
	for _, m := range manifests {
		if b, err := os.ReadFile(filepath.Join(rp.Repo.Path, filepath.FromSlash(m))); err == nil && strings.Contains(string(b), "[workspace]") {
			workspaces = append(workspaces, path.Dir(m))
			linked = append(linked, filepath.Join(rp.Repo.Path, filepath.FromSlash(m)))
		}
	}
	for _, m := range manifests {
		dir, member := path.Dir(m), false
		for _, w := range workspaces {
			if w == "." || dir == w || strings.HasPrefix(dir, w+"/") {
				member = true
				break
			}
		}
		if !member {
			linked = append(linked, filepath.Join(rp.Repo.Path, filepath.FromSlash(m)))
		}
	}
	if len(linked) == 0 {
		return nil
	}
	return map[string]any{"rust-analyzer": map[string]any{"linkedProjects": linked}}
}

// Telling language servers what changed on disk. An editor watches its workspace and reports every
// change (workspace/didChangeWatchedFiles), or a server's view of the folder goes stale: a file a branch
// switch rewrote, one a terminal or a build created or deleted. The folder's watcher already sees each
// one (see index/live.go); once a server runs for the folder, every batch is passed on.
type lspWatch struct {
	sync.Mutex
	feeds map[string]lspFeed
}

type lspFeed struct {
	x    *index.Index
	stop func()
}

// watchForServers passes the folder's file changes to its language servers (once per index).
func (s *Server) watchForServers(rp *workspace.Repo) {
	x := s.index(rp)
	w := &s.lspWatch
	w.Lock()
	defer w.Unlock()
	if w.feeds == nil {
		w.feeds = map[string]lspFeed{}
	}
	if f, ok := w.feeds[rp.ID]; ok && f.x == x {
		return
	} else if ok {
		f.stop()
	}
	root := rp.Repo.Path
	stop := x.OnFiles(func(events []index.FileEvent) {
		if !s.lsps.Serving(root) {
			return
		}
		changes := make([]lsp.Change, len(events))
		for i, e := range events {
			changes[i] = lsp.Change{Path: filepath.Join(root, filepath.FromSlash(e.Path)), Kind: e.Kind}
		}
		s.lsps.FilesChanged(root, changes)
	})
	w.feeds[rp.ID] = lspFeed{x: x, stop: stop}
	x.Watch()
}
