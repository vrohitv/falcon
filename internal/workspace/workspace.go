// Package workspace is the set of folders one Falcon server serves. Each folder is a Git repository with a
// stable ID (its name and a short hash of its path), so a URL like /r/<id>/ always means the same folder
// and every browser tab can be on a different one. The list is saved, so it survives a restart.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vrohitv/falcon/internal/sysenv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/repository"
)

// Repo is one folder.
type Repo struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Path  string                 `json:"path"`
	Added time.Time              `json:"added"`
	Repo  *repository.Repository `json:"-"`
}

// Workspace holds the folders.
type Workspace struct {
	file  string
	mu    sync.RWMutex
	repos map[string]*Repo
	order []string
	// missing are saved folders that could not be opened this time; they stay in the file.
	missing []savedFolder
}

type saved struct {
	Folders []savedFolder `json:"folders"`
}
type savedFolder struct {
	Path  string    `json:"path"`
	Added time.Time `json:"added"`
}

// ErrNotGit reports a folder that is not inside a Git repository.
var ErrNotGit = errors.New("not a Git repository")

// Open reads the saved list from file (missing is empty). Folders that have gone away are skipped, not
// forgotten, so a disk that is not mounted today comes back tomorrow.
func Open(file string) (*Workspace, error) {
	w := &Workspace{file: file, repos: map[string]*Repo{}}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	for _, f := range s.Folders {
		if r, err := load(f.Path); err == nil {
			r.Added = f.Added
			w.put(r)
		} else {
			w.missing = append(w.missing, f)
		}
	}
	return w, nil
}

// List is every folder, in the order they were added.
func (w *Workspace) List() []*Repo {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]*Repo, 0, len(w.order))
	for _, id := range w.order {
		out = append(out, w.repos[id])
	}
	return out
}

// Get is the folder with this ID.
func (w *Workspace) Get(id string) (*Repo, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	r, ok := w.repos[id]
	return r, ok
}

// Add serves the repository containing path (a subfolder adds the whole repository). Adding one already
// served returns it. With init, a folder that is not a repository becomes one (git init).
func (w *Workspace) Add(path string, init bool) (*Repo, error) {
	path = expand(strings.TrimSpace(path))
	if path == "" {
		return nil, errors.New("enter a folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%s: no such folder", abs)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is a file, not a folder", abs)
	}
	top, err := toplevel(abs)
	if errors.Is(err, ErrNotGit) && init {
		if out, e := gitIn(abs, "init", "-q"); e != nil {
			return nil, fmt.Errorf("git init: %s", strings.TrimSpace(out))
		}
		top, err = toplevel(abs)
	}
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	for _, r := range w.repos {
		if r.Path == top {
			w.mu.RUnlock()
			return r, nil
		}
	}
	w.mu.RUnlock()
	r, err := load(top)
	if err != nil {
		return nil, err
	}
	r.Added = time.Now().UTC()
	w.mu.Lock()
	w.put(r)
	w.mu.Unlock()
	return r, w.save()
}

// Remove stops serving a folder (its files are untouched).
func (w *Workspace) Remove(id string) error {
	w.mu.Lock()
	if _, ok := w.repos[id]; !ok {
		w.mu.Unlock()
		return errors.New("no such folder")
	}
	delete(w.repos, id)
	for i, x := range w.order {
		if x == id {
			w.order = append(w.order[:i], w.order[i+1:]...)
			break
		}
	}
	w.mu.Unlock()
	return w.save()
}

func (w *Workspace) put(r *Repo) {
	if _, ok := w.repos[r.ID]; !ok {
		w.order = append(w.order, r.ID)
	}
	w.repos[r.ID] = r
}

func (w *Workspace) save() error {
	if w.file == "" {
		return nil
	}
	w.mu.RLock()
	s := saved{Folders: []savedFolder{}}
	for _, id := range w.order {
		s.Folders = append(s.Folders, savedFolder{Path: w.repos[id].Path, Added: w.repos[id].Added})
	}
	s.Folders = append(s.Folders, w.missing...)
	w.mu.RUnlock()
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(w.file), 0o700); err != nil {
		return err
	}
	tmp := w.file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.file)
}

func load(path string) (*Repo, error) {
	repo, err := repository.Open(path)
	if err != nil {
		return nil, err
	}
	return &Repo{ID: ID(repo.Path), Name: filepath.Base(repo.Path), Path: repo.Path, Repo: repo}, nil
}

var unsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// ID is a folder's stable identifier: its name, readable in a URL, and a hash of its full path, so two
// folders with one name stay apart.
func ID(path string) string {
	name := strings.Trim(unsafe.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-"), "-.")
	if name == "" {
		name = "repo"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	sum := sha256.Sum256([]byte(path))
	return name + "-" + hex.EncodeToString(sum[:])[:6]
}

func toplevel(dir string) (string, error) {
	out, err := gitIn(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if strings.Contains(out, "not a git repository") {
			return "", fmt.Errorf("%s: %w", dir, ErrNotGit)
		}
		return "", errors.New(strings.TrimSpace(out))
	}
	top := strings.TrimSpace(out)
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top, nil
}

func gitIn(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = sysenv.Git()
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// Folder is an entry when browsing for a folder to add.
type Folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Git  bool   `json:"git"`
}

// Browse lists the folders inside dir (default: the home folder), marking repositories, for the
// "Open folder" picker. Hidden folders are left out.
func Browse(dir string) (string, []Folder, error) {
	dir = expand(strings.TrimSpace(dir))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		dir = home
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return abs, nil, err
	}
	out := []Folder{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(abs, e.Name())
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		_, gitErr := os.Stat(filepath.Join(full, ".git"))
		out = append(out, Folder{Name: e.Name(), Path: full, Git: gitErr == nil})
		if len(out) == 500 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return abs, out, nil
}
