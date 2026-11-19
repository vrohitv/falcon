package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/repository"
	"github.com/vrohitv/falcon/internal/workspace"
)

// Files outside a folder that a language server pointed to: a definition in the standard library or a
// dependency (Go's module cache, a crate, node_modules elsewhere). They open read-only, as
// ~ext/<token>/<readable path>; only files a language server named for this folder are reachable, by a
// random token Falcon issued, so this is not a way to read any file on the machine.
const extPrefix = "~ext/"

type externalFiles struct {
	sync.Mutex
	byToken map[string]externalFile // repo ID + "/" + token
	byPath  map[string]string       // repo ID + "\x00" + abs → token
}

type externalFile struct {
	abs, display string
	at           time.Time
}

// add registers a file a language server named, giving the path to open it by ("" if it cannot be).
func (e *externalFiles) add(repoID, abs string) string {
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return ""
	}
	e.Lock()
	defer e.Unlock()
	if e.byToken == nil {
		e.byToken, e.byPath = map[string]externalFile{}, map[string]string{}
	}
	token, ok := e.byPath[repoID+"\x00"+abs]
	if !ok {
		b := make([]byte, 9)
		_, _ = rand.Read(b)
		token = hex.EncodeToString(b)
		e.byPath[repoID+"\x00"+abs] = token
		if len(e.byToken) > 5000 { // forget the oldest half
			cut := time.Now()
			for _, f := range e.byToken {
				if f.at.Before(cut) {
					cut = f.at
				}
			}
			for k, f := range e.byToken {
				if f.at.Sub(cut) < time.Hour {
					delete(e.byToken, k)
					delete(e.byPath, repoID+"\x00"+f.abs)
				}
			}
		}
	}
	f := externalFile{abs: abs, display: externalDisplay(abs), at: time.Now()}
	e.byToken[repoID+"/"+token] = f
	return extPrefix + token + "/" + f.display
}

// lookup is the file behind an ~ext/ path.
func (e *externalFiles) lookup(repoID, path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, extPrefix)
	if !ok {
		return "", false
	}
	token, _, _ := strings.Cut(rest, "/")
	e.Lock()
	defer e.Unlock()
	f, ok := e.byToken[repoID+"/"+token]
	return f.abs, ok
}

// externalDisplay is a readable name for a file outside the folder: Go's standard library and module
// cache, Rust's toolchain and crates, else relative to home.
func externalDisplay(abs string) string {
	p := filepath.ToSlash(abs)
	for _, cut := range []struct{ marker, label string }{
		{"/pkg/mod/", ""},                     // github.com/x/y@v1.2.3/file.go
		{"/libexec/src/", "go/"},              // Homebrew Go's standard library
		{"/go/src/", "go/"},                   // the standard library
		{"/registry/src/", "crates/"},         // cargo registry
		{"/lib/rustlib/src/rust/", "rust/"},   // Rust's standard library
		{"/node_modules/", "node_modules/"},   // a package outside the folder
		{"/site-packages/", "site-packages/"}, // a Python package
	} {
		if i := strings.LastIndex(p, cut.marker); i >= 0 {
			return cut.label + p[i+len(cut.marker):]
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return strings.TrimPrefix(p, "/")
}

// readable is the absolute path of a file to read: one in the folder (checked by the repository), or an
// outside file a language server named.
func (s *Server) readable(rp *workspace.Repo, path string) (string, error) {
	if strings.HasPrefix(path, extPrefix) {
		if abs, ok := s.external.lookup(rp.ID, path); ok {
			return abs, nil
		}
		return "", errors.New("this file outside the folder is no longer available; go to its definition again")
	}
	return rp.Repo.ReadPath(path)
}

// readContent reads a file (in the folder, or an outside one a language server named) with its hash.
func (s *Server) readContent(rp *workspace.Repo, path string) (repository.FileContent, string, error) {
	if !strings.HasPrefix(path, extPrefix) {
		f, err := rp.Repo.Read(path)
		return f, filepath.Join(rp.Repo.Path, filepath.FromSlash(path)), err
	}
	abs, err := s.readable(rp, path)
	if err != nil {
		return repository.FileContent{}, "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return repository.FileContent{}, "", err
	}
	return repository.FileContent{Path: path, Content: string(b), Hash: domain.Hash(b)}, abs, nil
}
