package server

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
// change (workspace/didChangeWatchedFiles), or a server's view of the repository goes stale: a file a
// branch switch rewrote, one a terminal or a build created or deleted. Falcon already watches each folder
// with git (the working tree's status, and HEAD); between two looks, the files whose status or contents
// changed, and on a new HEAD the files that differ between the two commits, are reported to the folder's
// running servers.
type treeSnapshot struct {
	head  string
	files map[string]string // path → status and size/time
}

type lspWatch struct {
	sync.Mutex
	snaps map[string]*treeSnapshot
	busy  map[string]bool
}

// watchForServers takes the folder's first snapshot, once a server runs for it.
func (s *Server) watchForServers(rp *workspace.Repo) {
	s.lspWatch.Lock()
	_, ok := s.lspWatch.snaps[rp.ID]
	s.lspWatch.Unlock()
	if !ok {
		go s.reportChanges(rp)
	}
}

// reportChanges compares the folder with its last snapshot and tells its servers what changed.
func (s *Server) reportChanges(rp *workspace.Repo) {
	if !s.lsps.Serving(rp.Repo.Path) {
		return
	}
	w := &s.lspWatch
	w.Lock()
	if w.busy == nil {
		w.busy, w.snaps = map[string]bool{}, map[string]*treeSnapshot{}
	}
	if w.busy[rp.ID] {
		w.Unlock()
		return
	}
	w.busy[rp.ID] = true
	prev := w.snaps[rp.ID]
	w.Unlock()
	defer func() {
		w.Lock()
		w.busy[rp.ID] = false
		w.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := &treeSnapshot{files: map[string]string{}}
	if head, err := rp.Repo.Git(ctx, "rev-parse", "--verify", "HEAD"); err == nil {
		now.head = strings.TrimSpace(head)
	}
	out, err := rp.Repo.Git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return
	}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		rec := parts[i]
		if len(rec) < 4 {
			continue
		}
		p := rec[3:]
		sig := rec[:2]
		if info, err := os.Lstat(filepath.Join(rp.Repo.Path, filepath.FromSlash(p))); err == nil {
			sig += ":" + info.ModTime().UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(info.Size(), 10)
		}
		now.files[p] = sig
		if strings.ContainsAny(rec[:2], "RC") {
			i++ // the rename's old path
			if i < len(parts) && parts[i] != "" {
				now.files[parts[i]] = "renamed"
			}
		}
	}
	w.Lock()
	w.snaps[rp.ID] = now
	w.Unlock()
	if prev == nil {
		return
	}
	changed := map[string]bool{}
	for p, sig := range now.files {
		if prev.files[p] != sig {
			changed[p] = true
		}
	}
	for p := range prev.files {
		if _, ok := now.files[p]; !ok {
			changed[p] = true
		}
	}
	if prev.head != "" && now.head != "" && prev.head != now.head {
		if diff, err := rp.Repo.Git(ctx, "diff", "--name-only", "-z", "--no-renames", prev.head, now.head, "--"); err == nil {
			for _, p := range strings.Split(diff, "\x00") {
				if p != "" {
					changed[p] = true
				}
			}
		}
	}
	changes := make([]lsp.Change, 0, len(changed))
	for p := range changed {
		abs := filepath.Join(rp.Repo.Path, filepath.FromSlash(p))
		kind := 2
		if _, err := os.Lstat(abs); err != nil {
			kind = 3
		} else if _, before := prev.files[p]; !before && strings.HasPrefix(now.files[p], "??") {
			kind = 1
		}
		changes = append(changes, lsp.Change{Path: abs, Kind: kind})
	}
	s.lsps.FilesChanged(rp.Repo.Path, changes)
}
