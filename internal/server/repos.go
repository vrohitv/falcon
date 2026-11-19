package server

import (
	"net/http"
	"path/filepath"

	"github.com/vrohitv/falcon/internal/workspace"
)

type repoView struct {
	*workspace.Repo
	Branch string `json:"branch"`
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request) {
	out := []repoView{}
	for _, rp := range s.Workspace.List() {
		out = append(out, repoView{rp, rp.Repo.Branch(r.Context())})
	}
	respond(w, map[string]any{"repos": out, "version": Version})
}

func (s *Server) addRepo(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
		Init bool   `json:"init"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	rp, err := s.Workspace.Add(input.Path, input.Init)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, repoView{rp, rp.Repo.Branch(r.Context())})
}

func (s *Server) removeRepo(w http.ResponseWriter, r *http.Request) {
	rp, ok := s.Workspace.Get(r.PathValue("repo"))
	if ok {
		defer s.forget(rp)
	}
	if err := s.Workspace.Remove(r.PathValue("repo")); err != nil {
		fail(w, err)
		return
	}
	respond(w, map[string]bool{"ok": true})
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	dir, folders, err := workspace.Browse(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	// The folder above, as this system writes paths ("" at the top: / or C:\).
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	respond(w, map[string]any{"path": dir, "parent": parent, "folders": folders})
}
