package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	git "github.com/go-git/go-git/v5"
	"github.com/vrohitv/falcon/internal/sysenv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/domain"
)

const MaxFileSize = 2 << 20

type Repository struct {
	Path string
	mu   sync.Mutex
	// gogit is the kept-open go-git handle (see native).
	gogit struct {
		sync.Mutex
		repo *git.Repository
		// use serializes reads through the handle: go-git's pack readers are not safe to share.
		use sync.Mutex
	}
	// filters caches which content filters the configuration declares (they are turned off on every git
	// call). It is checked again whenever a configuration file changes, so it is never stale.
	filters struct {
		sync.Mutex
		gitDir string
		key    [2]string
		keys   [2][]byte
		core   []string
		// The author and remotes, and the state of the configuration files they were read at.
		settings    gitSettings
		settingsKey string
		settingsAt  time.Time
	}
}
type File struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Language string `json:"language"`
}
type FileContent struct {
	Hash    string `json:"hash"`
	Path    string `json:"path"`
	Content string `json:"content"`
}
type Status struct {
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Changes string `json:"changes"`
	Clean   bool   `json:"clean"`
}

func Open(path string) (*Repository, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	r := &Repository{Path: p}
	ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	top, err := r.Git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("repository must be initialized with git: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return nil, err
	}
	if canonical != p {
		return nil, errors.New("repository path must identify the Git worktree root")
	}
	return r, nil
}
func (r *Repository) Git(ctx context.Context, args ...string) (string, error) {
	cmd, err := r.gitCommand(ctx, args...)
	if err != nil {
		return "", err
	}
	b, err := cmd.CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("git: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return string(b), nil
}

// Git is a control-plane dependency: repository-configured filters must not
// execute code or inherit provider credentials during snapshots or checkout.
func (r *Repository) gitCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return r.gitCommandAs(ctx, false, args...)
}

// filterKeys lists the filter settings in the configuration git will read. Asking git costs a process,
// so the answer is kept until one of the configuration files changes (by size or time).
func (r *Repository) filterKeys(ctx context.Context, owner bool, env []string) ([]byte, error) {
	f := &r.filters
	f.Lock()
	defer f.Unlock()
	if f.gitDir == "" {
		cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir", "--git-dir")
		cmd.Dir, cmd.Env = r.Path, env
		if out, err := cmd.Output(); err == nil {
			f.gitDir = strings.TrimSpace(string(out))
		}
	}
	files := []string{}
	for _, d := range strings.Split(f.gitDir, "\n") {
		files = append(files, filepath.Join(d, "config"), filepath.Join(d, "config.worktree"))
	}
	which := 0
	if owner {
		which = 1
		home := os.Getenv("HOME")
		files = append(files, filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git", "config"))
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			files = append(files, filepath.Join(x, "git", "config"))
		}
	}
	var key strings.Builder
	for _, name := range files {
		if info, err := os.Stat(name); err == nil {
			fmt.Fprintf(&key, "%s:%d:%d;", name, info.Size(), info.ModTime().UnixNano())
		}
	}
	if f.gitDir != "" && f.key[which] == key.String() && f.keys[which] != nil {
		return f.keys[which], nil
	}
	config := exec.CommandContext(ctx, "git", "config", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	config.Dir, config.Env = r.Path, env
	keys, err := config.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("inspect git filters: %w", err)
		}
	}
	if keys == nil {
		keys = []byte{}
	}
	f.key[which], f.keys[which] = key.String(), keys
	// The storage settings, as the owner's own git sees them (system, global and repository config).
	core := exec.CommandContext(ctx, "git", "config", "--null", "--get-regexp", `^(core\.(autocrlf|eol|safecrlf|filemode|symlinks|ignorecase|precomposeunicode|longpaths|checkroundtripencoding|fscache|preloadindex|untrackedcache|commitgraph|multipackindex)|feature\.manyfiles|index\.(threads|skiphash))$`)
	core.Dir, core.Env = r.Path, sysenv.Base()
	f.core = nil
	if out, err := core.Output(); err == nil {
		for _, kv := range strings.Split(string(out), "\x00") {
			name, value, ok := strings.Cut(kv, "\n")
			if ok && name != "" {
				f.core = append(f.core, "-c", name+"="+value)
			}
		}
	}
	return keys, nil
}

// remoteEnv are the variables git needs from the owner to reach a remote the way their terminal does:
// the SSH agent, proxies, where their config and credential helpers live.
var remoteEnv = []string{"USER", "LOGNAME", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GIT_SSH_COMMAND", "GIT_SSH", "XDG_CONFIG_HOME",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "ALL_PROXY", "GH_CONFIG_DIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "TMPDIR"}

// gitCommandAs builds a git command. With owner set, git reads the owner's own (global) configuration and
// SSH agent, as it must to push, pull and fetch with their credentials; prompts still never block it
// (no terminal: a missing credential is an error, not a hang). Hooks and content filters stay off.
func (r *Repository) gitCommandAs(ctx context.Context, owner bool, args ...string) (*exec.Cmd, error) {
	// No optional locks: a background status must never hold the index lock
	// and make the owner's own stage or commit fail.
	env := sysenv.Git()
	if owner {
		env = sysenv.Base("GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GCM_INTERACTIVE=never")
		for _, k := range remoteEnv {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
		if os.Getenv("GIT_SSH_COMMAND") == "" {
			// An unknown host or a passphrase prompt fails instead of waiting for a terminal that is not there.
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=20")
		}
	}
	keys, err := r.filterKeys(ctx, owner, env)
	if err != nil {
		return nil, err
	}
	base := []string{"-c", "core.hooksPath=" + sysenv.Null, "-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + sysenv.Null, "-c", "diff.external=", "-c", "protocol.file.allow=never", "--no-pager"}
	if owner {
		// A remote may be a folder (a bare repository on a disk, offline).
		base = []string{"-c", "core.hooksPath=" + sysenv.Null, "-c", "core.fsmonitor=false", "-c", "diff.external=", "--no-pager"}
	}
	if !owner {
		// Falcon reads none of the owner's global or system configuration, except how files are stored on
		// this machine: line endings (Git for Windows sets core.autocrlf system-wide) and the filesystem's
		// habits. Without these a Windows checkout would look entirely modified.
		base = append(base, r.filters.core...)
	}
	for _, key := range strings.Split(string(keys), "\x00") {
		if key == "" {
			continue
		}
		value := ""
		if strings.HasSuffix(key, ".required") {
			value = "false"
		}
		base = append(base, "-c", key+"="+value)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir, cmd.Env = r.Path, env
	return cmd, nil
}

// Only Git's own folder is off limits: Falcon is the owner's viewer, and what is in the folder is theirs.
var protectedComponents = []string{".git"}

func Protected(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		for _, pattern := range protectedComponents {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}
	return false
}
func ValidPath(path string) error {
	if path == "" || !filepath.IsLocal(path) || strings.Contains(path, "\\") || Protected(path) {
		return errors.New("path must be a repository-relative, non-protected path")
	}
	return nil
}
func (r *Repository) securePath(path string) (string, error) {
	if err := ValidPath(path); err != nil {
		return "", err
	}
	full := filepath.Join(r.Path, path)
	cur := r.Path
	for _, p := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A link is followed only when where it leads is inside the folder (and not Git's own).
			if target, ok := r.inside(full); ok {
				return target, nil
			}
			return "", errors.New("this link points outside the folder")
		}
	}
	return full, nil
}

// inside resolves every link in abs and reports whether the result is in the folder, outside .git.
func (r *Repository) inside(abs string) (string, bool) {
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r.Path, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) || Protected(filepath.ToSlash(rel)) {
		return "", false
	}
	return target, true
}

// ReadPath validates every path component before handing a file to the windowed
// IDE reader. Protected files and symlinks remain outside the viewer surface.
func (r *Repository) ReadPath(path string) (string, error) { return r.securePath(path) }
func (r *Repository) Read(path string) (FileContent, error) {
	b, err := r.ReadText(path)
	if err != nil {
		return FileContent{}, err
	}
	return FileContent{Path: path, Content: string(b), Hash: domain.Hash(b)}, nil
}

// ReadText is Read without hashing, for scans that hash only what they keep.
func (r *Repository) ReadText(path string) ([]byte, error) {
	full, err := r.securePath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileSize {
		return nil, errors.New("file exceeds 2 MiB editor limit")
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, errors.New("binary files cannot be opened as text")
	}
	return b, nil
}
func (r *Repository) Write(path, content, expectedHash string) (FileContent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.write(path, content, expectedHash)
}
func (r *Repository) write(path, content, expectedHash string) (FileContent, error) {
	full, err := r.securePath(path)
	if err != nil {
		return FileContent{}, err
	}
	if len(content) > MaxFileSize {
		return FileContent{}, errors.New("file exceeds 2 MiB")
	}
	old, err := os.ReadFile(full)
	if err != nil && !os.IsNotExist(err) {
		return FileContent{}, err
	}
	if (err == nil && domain.Hash(old) != expectedHash) || (os.IsNotExist(err) && expectedHash != "") {
		return FileContent{}, domain.ErrConflict
	}
	if err = os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return FileContent{}, err
	}
	mode := os.FileMode(0644)
	if info, e := os.Stat(full); e == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(full), ".falcon-edit-")
	if err != nil {
		return FileContent{}, err
	}
	defer os.Remove(f.Name())
	_ = f.Chmod(mode)
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Sync()
	}
	_ = f.Close()
	if err != nil {
		return FileContent{}, err
	}
	if err = os.Rename(f.Name(), full); err != nil {
		return FileContent{}, err
	}
	if err = syncDir(filepath.Dir(full)); err != nil {
		return FileContent{}, err
	}
	return FileContent{Path: path, Content: content, Hash: domain.Hash([]byte(content))}, nil
}
func Language(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx", ".ipp":
		return "cpp"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".java":
		return "java"
	case ".go":
		return "go"
	default:
		return "text"
	}
}
func (r *Repository) Files(ctx context.Context) ([]File, error) {
	out, err := r.Git(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	// Each directory is checked for symlinks once, not once per file below
	// it; a listing costs about one syscall per file.
	dirs := map[string]bool{".": true}
	var safeDir func(string) bool
	safeDir = func(dir string) bool {
		if ok, known := dirs[dir]; known {
			return ok
		}
		ok := safeDir(filepath.Dir(dir))
		if ok {
			info, err := os.Lstat(filepath.Join(r.Path, dir))
			ok = err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
		}
		dirs[dir] = ok
		return ok
	}
	seen := map[string]bool{}
	files := []File{}
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || seen[p] || ValidPath(p) != nil {
			continue
		}
		seen[p] = true
		if !safeDir(filepath.Dir(p)) {
			continue
		}
		info, e := os.Lstat(filepath.Join(r.Path, p))
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			// A link to a file inside the folder is listed (under its own name).
			if target, ok := r.inside(filepath.Join(r.Path, p)); ok {
				info, e = os.Stat(target)
			} else {
				continue
			}
		}
		if e != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, File{Path: p, Size: info.Size(), Language: Language(p)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Status is the branch, the current commit and the changed files, from one git process.
func (r *Repository) Status(ctx context.Context) Status {
	out, err := r.Git(ctx, "status", "--porcelain=v2", "--branch")
	st := Status{}
	if err != nil {
		st.Branch = r.Branch(ctx)
		return st
	}
	var changes strings.Builder
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			if oid := strings.TrimPrefix(line, "# branch.oid "); oid != "(initial)" {
				st.Head = oid
			}
		case strings.HasPrefix(line, "# branch.head "):
			if head := strings.TrimPrefix(line, "# branch.head "); head != "(detached)" {
				st.Branch = head
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			changes.WriteString(line + "\n")
		}
	}
	st.Changes = changes.String()
	st.Clean = st.Changes == ""
	return st
}

// Branch is the checked-out branch, read from HEAD without starting git ("" when detached).
func (r *Repository) Branch(ctx context.Context) string {
	_, dir := r.gitDirs(ctx)
	b, err := os.ReadFile(filepath.Join(dir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return ref
}
func (r *Repository) History(ctx context.Context, path string) (string, error) {
	args := []string{"log", "-40", "--date=iso-strict", "--format=%H%x09%ad%x09%an%x09%s", "--name-only"}
	if path != "" {
		if err := ValidPath(path); err != nil {
			return "", err
		}
		args = append(args, "--", path)
	}
	return r.Git(ctx, args...)
}
func (r *Repository) Show(ctx context.Context, commit, path string) (string, error) {
	if err := ValidPath(path); err != nil {
		return "", err
	}
	if !ValidCommit(commit) {
		return "", errors.New("a full commit hash is required")
	}
	return r.Git(ctx, "show", commit+":"+filepath.ToSlash(path))
}
func ValidCommit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// Snapshot materializes the exact Git tree through a temporary index. The user's
// index is never touched. Git records mode bits, symlinks and untracked files.
