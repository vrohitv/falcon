package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/vrohitv/falcon/internal/sysenv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vrohitv/falcon/internal/domain"
)

type GitFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Index     string `json:"index"`
	Worktree  string `json:"worktree"`
	Conflict  bool   `json:"conflict"`
	Protected bool   `json:"protected"`
}
type GitBranch struct {
	Name     string `json:"name"`
	Head     string `json:"head"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream"`
	Worktree string `json:"worktree"`
	Managed  bool   `json:"managed"`
}
type GitState struct {
	Branch      string      `json:"branch"`
	Head        string      `json:"head"`
	Revision    string      `json:"revision"`
	IndexHash   string      `json:"index_hash"`
	Clean       bool        `json:"clean"`
	Detached    bool        `json:"detached"`
	Operation   string      `json:"operation"`
	Files       []GitFile   `json:"files"`
	Branches    []GitBranch `json:"branches"`
	AuthorName  string      `json:"author_name"`
	AuthorEmail string      `json:"author_email"`
	// Where the branch stands against its upstream, and what else there is to work with.
	Upstream       string      `json:"upstream"`
	Ahead          int         `json:"ahead"`
	Behind         int         `json:"behind"`
	HeadPushed     bool        `json:"head_pushed"`
	Remotes        []GitRemote `json:"remotes"`
	RemoteBranches []string    `json:"remote_branches"`
	Stashes        []GitStash  `json:"stashes"`
	LastMessage    string      `json:"last_message"`
	MergeMessage   string      `json:"merge_message,omitempty"`
}
type GitRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type GitStash struct {
	Ref     string `json:"ref"`
	Message string `json:"message"`
	When    string `json:"when"`
}
type GitRequest struct {
	ID          string   `json:"id"`
	Action      string   `json:"action"`
	Revision    string   `json:"revision"`
	Paths       []string `json:"paths,omitempty"`
	Message     string   `json:"message,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	AuthorName  string   `json:"author_name,omitempty"`
	AuthorEmail string   `json:"author_email,omitempty"`
	// Amend the last commit; commit every change when nothing is staged.
	Amend bool `json:"amend,omitempty"`
	All   bool `json:"all,omitempty"`
	// Force: delete an unmerged branch, push with lease.
	Force            bool   `json:"force,omitempty"`
	Remote           string `json:"remote,omitempty"`
	URL              string `json:"url,omitempty"`
	Stash            string `json:"stash,omitempty"`
	NewName          string `json:"new_name,omitempty"`
	IncludeUntracked bool   `json:"include_untracked,omitempty"`
	// Hunk is a review hunk's ID, for hunk.* and lines.* actions; Lines are the chosen lines of it
	// (indices into the hunk's body) for lines.stage, lines.unstage and lines.discard.
	Hunk  string `json:"hunk,omitempty"`
	Lines []int  `json:"lines,omitempty"`
}

// The client uses literal paths and never lets read-only status refresh the index.
func (r *Repository) clientGit(ctx context.Context, index string, args ...string) (string, error) {
	return r.clientGitWith(ctx, false, index, nil, args...)
}

// ownerGit runs git as the owner would from a terminal: their configuration, credentials and SSH agent.
func (r *Repository) ownerGit(ctx context.Context, env []string, args ...string) (string, error) {
	return r.clientGitWith(ctx, true, "", env, args...)
}

func (r *Repository) clientGitWith(ctx context.Context, owner bool, index string, env []string, args ...string) (string, error) {
	cmd, err := r.gitCommandAs(ctx, owner, args...)
	if err != nil {
		return "", err
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1")
	cmd.Env = append(cmd.Env, env...)
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}
func (r *Repository) GitState(ctx context.Context) (GitState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gitState(ctx)
}
func (r *Repository) gitState(ctx context.Context) (GitState, error) {
	state := GitState{Files: []GitFile{}, Branches: []GitBranch{}, Remotes: []GitRemote{}, RemoteBranches: []string{}, Stashes: []GitStash{}}
	// Three questions, asked at once (on Windows every process start is expensive):
	//   status --branch: the files, the branch, its upstream and how far ahead or behind;
	//   ls-files: what is staged, exactly (for the revision);
	//   for-each-ref: every branch and remote branch, and the current commit and its message.
	var raw, entries, refs string
	var rawErr, entriesErr, refsErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		raw, rawErr = r.clientGit(ctx, "", "-c", "status.renames=true", "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all")
	}()
	go func() { defer wg.Done(); entries, entriesErr = r.clientGit(ctx, "", "ls-files", "--stage", "-z") }()
	go func() {
		defer wg.Done()
		refs, refsErr = r.clientGit(ctx, "", "for-each-ref", "--sort=refname",
			"--format=%(refname)%00%(objectname)%00%(upstream:short)%00%(worktreepath)%00%(HEAD)%00%(if)%(HEAD)%(then)%(contents)%(end)%1e",
			"refs/heads/", "refs/remotes/")
	}()
	wg.Wait()
	if rawErr != nil {
		return state, rawErr
	}
	if entriesErr != nil {
		return state, entriesErr
	}
	if refsErr != nil {
		return state, refsErr
	}
	parts := strings.Split(raw, "\x00")
	// "## main...origin/main [ahead 1, behind 2]", "## No commits yet on main", "## HEAD (no branch)".
	if len(parts) > 0 && strings.HasPrefix(parts[0], "## ") {
		line := strings.TrimPrefix(parts[0], "## ")
		parts = parts[1:]
		switch {
		case strings.HasPrefix(line, "No commits yet on "):
			state.Branch = strings.TrimPrefix(line, "No commits yet on ")
		case strings.HasPrefix(line, "Initial commit on "):
			state.Branch = strings.TrimPrefix(line, "Initial commit on ")
		case strings.HasPrefix(line, "HEAD (no branch)"):
			state.Detached = true
		default:
			track := ""
			if i := strings.Index(line, " ["); i >= 0 {
				line, track = line[:i], strings.TrimSuffix(line[i+2:], "]")
			}
			state.Branch, state.Upstream, _ = strings.Cut(line, "...")
			for _, part := range strings.Split(track, ", ") {
				if n, ok := strings.CutPrefix(part, "ahead "); ok {
					state.Ahead, _ = strconv.Atoi(n)
				} else if n, ok := strings.CutPrefix(part, "behind "); ok {
					state.Behind, _ = strconv.Atoi(n)
				}
			}
		}
	}
	for _, rec := range strings.Split(refs, "\x1e") {
		p := strings.Split(strings.TrimLeft(rec, "\n"), "\x00")
		if len(p) != 6 {
			continue
		}
		name, current := p[0], p[4] == "*"
		if short, ok := strings.CutPrefix(name, "refs/heads/"); ok {
			state.Branches = append(state.Branches, GitBranch{Name: short, Head: p[1], Upstream: p[2], Worktree: p[3], Current: current, Managed: managedBranch(short)})
			if current {
				state.Head, state.LastMessage = p[1], strings.TrimSpace(p[5])
			}
		} else if short, ok := strings.CutPrefix(name, "refs/remotes/"); ok && !strings.HasSuffix(short, "/HEAD") {
			state.RemoteBranches = append(state.RemoteBranches, short)
		}
	}
	if state.Detached {
		// Only now is another process needed: the commit a detached HEAD is on.
		if out, err := r.clientGit(ctx, "", "log", "-1", "--format=%H%x00%B", "HEAD", "--"); err == nil {
			h, msg, _ := strings.Cut(out, "\x00")
			state.Head, state.LastMessage = strings.TrimSpace(h), strings.TrimSpace(msg)
		}
	}
	state.HeadPushed = state.Upstream != "" && state.Ahead == 0 && state.Head != ""
	state.IndexHash = domain.Hash([]byte(entries))
	var identity strings.Builder
	fmt.Fprintf(&identity, "%s\x00%s\x00%s\x00%s", state.Head, state.Branch, entries, strings.Join(parts, "\x00"))
	root, err := os.OpenRoot(r.Path)
	if err != nil {
		return state, err
	}
	defer root.Close()
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "" {
			continue
		}
		if len(p) < 4 {
			return state, errors.New("invalid Git status record")
		}
		f := GitFile{Path: p[3:], Index: strings.TrimSpace(p[:1]), Worktree: strings.TrimSpace(p[1:2])}
		if f.Index == "?" {
			f.Index = ""
		}
		if strings.ContainsAny(p[:2], "RC") {
			i++
			if i >= len(parts) {
				return state, errors.New("incomplete rename record")
			}
			f.OldPath = parts[i]
		}
		f.Conflict = strings.Contains(p[:2], "U") || p[:2] == "AA" || p[:2] == "DD"
		f.Protected = ValidPath(f.Path) != nil || (f.OldPath != "" && ValidPath(f.OldPath) != nil)
		state.Files = append(state.Files, f)
		if f.Protected {
			continue
		}
		// Hash only changed files, including changes whose porcelain status is unchanged.
		info, statErr := root.Lstat(f.Path)
		if errors.Is(statErr, os.ErrNotExist) {
			identity.WriteString("\x00deleted")
			continue
		}
		if statErr != nil {
			return state, statErr
		}
		fmt.Fprintf(&identity, "\x00%s:%s:", f.Path, info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := root.Readlink(f.Path)
			if e != nil {
				return state, e
			}
			identity.WriteString(target)
		} else if info.Mode().IsRegular() {
			file, e := root.Open(f.Path)
			if e != nil {
				return state, e
			}
			hash := sha256.New()
			_, e = io.Copy(hash, file)
			file.Close()
			if e != nil {
				return state, e
			}
			identity.WriteString(hex.EncodeToString(hash.Sum(nil)))
		}
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
	}
	state.Clean = len(state.Files) == 0
	state.Revision = domain.Hash([]byte(identity.String()))
	common, dir := r.gitDirs(ctx)
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			state.Operation = name
			break
		}
	}
	if inProgress(state.Operation) {
		if b, err := os.ReadFile(filepath.Join(dir, "MERGE_MSG")); err == nil {
			lines := []string{}
			for _, l := range strings.Split(string(b), "\n") {
				if !strings.HasPrefix(l, "#") {
					lines = append(lines, l)
				}
			}
			state.MergeMessage = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	// The stash: only asked about when there is one.
	if _, err := os.Stat(filepath.Join(common, "logs", "refs", "stash")); err == nil {
		if out, err := r.clientGit(ctx, "", "stash", "list", "--format=%gd%x00%gs%x00%cr"); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if p := strings.Split(line, "\x00"); len(p) == 3 {
					state.Stashes = append(state.Stashes, GitStash{Ref: p[0], Message: p[1], When: p[2]})
				}
			}
		}
	}
	cfg := r.settings(ctx)
	state.AuthorName, state.AuthorEmail, state.Remotes = cfg.name, cfg.email, cfg.remotes
	return state, nil
}

func managedBranch(name string) bool { return false }

// ApplyGit serializes owner effects with other repository writes. Index changes
// are prepared privately and atomically published under Git's index lock.
func (r *Repository) ApplyGit(ctx context.Context, req GitRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Staging and committing take Git's index lock first and look at the repository once, under it: the
	// state they check is then the state they change (one git status, not one before and one after).
	var lock *gitIndexLock
	switch req.Action {
	case "stage", "unstage", "stage.all", "unstage.all", "commit":
		if req.Action != "commit" || !inProgress(r.operation(ctx)) {
			l, err := r.lockIndex(ctx)
			if err != nil {
				return "", err
			}
			defer l.release()
			lock = l
		}
	}
	state, err := r.gitState(ctx)
	if err != nil {
		return "", err
	}
	// A hunk carries its own check (it must still be exactly what was shown), so edits to other files
	// meanwhile do not get in the way.
	if strings.HasPrefix(req.Action, "hunk.") || strings.HasPrefix(req.Action, "lines.") {
		return r.applyHunk(ctx, req, state)
	}
	if req.Revision == "" || req.Revision != state.Revision {
		return "", fmt.Errorf("repository changed; refresh Source Control before retrying: %w", domain.ErrConflict)
	}
	switch req.Action {
	case "stage.all", "unstage.all":
		req.Action = strings.TrimSuffix(req.Action, ".all")
		req.Paths = nil
		for _, f := range state.Files {
			if !f.Protected && ((req.Action == "stage" && (f.Worktree != "" || f.Conflict)) || (req.Action == "unstage" && f.Index != "" && !f.Conflict)) {
				req.Paths = append(req.Paths, f.Path)
			}
		}
		if len(req.Paths) == 0 {
			return "", errors.New("nothing to " + req.Action)
		}
		return r.changeIndex(ctx, req, state, lock)
	case "commit":
		if inProgress(state.Operation) || lock == nil {
			return r.commitOperation(ctx, req, state)
		}
		return r.changeIndex(ctx, req, state, lock)
	case "stage", "unstage":
		return r.changeIndex(ctx, req, state, lock)
	case "branch.create", "branch.switch":
		if state.Operation != "" {
			return "", errors.New("finish the current Git operation before changing branches")
		}
		if req.Branch == "" || strings.HasPrefix(req.Branch, "-") || managedBranch(req.Branch) || strings.HasPrefix(req.Branch, "refs/") {
			return "", errors.New("enter a local branch name")
		}
		if _, err = r.clientGit(ctx, "", "check-ref-format", "--branch", req.Branch); err != nil {
			return "", errors.New("invalid branch name")
		}
		if state.Head == "" {
			return "", errors.New("create the first commit before creating or switching branches")
		}
		if req.Action == "branch.create" {
			_, err = r.clientGit(ctx, "", "update-ref", "--create-reflog", "-m", "falcon: create branch", "refs/heads/"+req.Branch, state.Head, strings.Repeat("0", len(state.Head)))
			return req.Branch, err
		}
		// Changes come along to the other branch, as in a terminal; git refuses the switch (and nothing is
		// lost) when they would be overwritten.
		var target *GitBranch
		for i := range state.Branches {
			if state.Branches[i].Name == req.Branch {
				target = &state.Branches[i]
				break
			}
		}
		if target == nil {
			return "", errors.New("local branch not found")
		}
		if target.Worktree != "" && filepath.Clean(target.Worktree) != filepath.Clean(r.Path) {
			return "", errors.New("branch is checked out in another worktree")
		}
		changed, e := r.clientGit(ctx, "", "diff", "--name-only", "-z", state.Head, target.Head, "--")
		if e != nil {
			return "", e
		}
		for _, path := range strings.Split(changed, "\x00") {
			if path != "" && Protected(path) {
				return "", errors.New("branch switch would change protected paths")
			}
		}
		if _, err = r.clientGit(ctx, "", "switch", "--no-guess", "--no-overwrite-ignore", req.Branch); err != nil {
			return "", friendlySwitch(err)
		}
		return req.Branch, nil
	default:
		return r.applyMore(ctx, req, state)
	}
}

// lockIndex takes Git's index lock. Another Git process (a terminal, an editor) usually holds it for
// milliseconds, so it waits briefly before reporting it as locked.
func (r *Repository) lockIndex(ctx context.Context) (*gitIndexLock, error) {
	_, dir := r.gitDirs(ctx)
	indexPath := filepath.Join(dir, "index")
	lock, err := acquireGitIndex(indexPath)
	for wait := 0; err != nil && wait < 60; wait++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		lock, err = acquireGitIndex(indexPath)
	}
	if err != nil {
		return nil, errors.New("Git index is locked by another operation; wait for it to finish")
	}
	return lock, nil
}

// fingerprints identify the given files' contents as they are now (type, size and a hash), to notice a
// file written to while it was being staged.
func (r *Repository) fingerprints(paths []string) string {
	root, err := os.OpenRoot(r.Path)
	if err != nil {
		return ""
	}
	defer root.Close()
	var b strings.Builder
	for _, p := range paths {
		info, err := root.Lstat(p)
		if err != nil {
			fmt.Fprintf(&b, "%s:-;", p)
			continue
		}
		fmt.Fprintf(&b, "%s:%s:%d:", p, info.Mode(), info.Size())
		if info.Mode().IsRegular() {
			if f, err := root.Open(p); err == nil {
				h := sha256.New()
				_, _ = io.Copy(h, f)
				f.Close()
				b.WriteString(hex.EncodeToString(h.Sum(nil)))
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			t, _ := root.Readlink(p)
			b.WriteString(t)
		}
		b.WriteString(";")
	}
	return b.String()
}

func (r *Repository) changeIndex(ctx context.Context, req GitRequest, state GitState, lock *gitIndexLock) (string, error) {
	if lock == nil {
		return "", errors.New("Git index is not locked")
	}
	indexPath := lock.index
	temp, err := os.CreateTemp(filepath.Dir(indexPath), "falcon-index-")
	if err != nil {
		return "", err
	}
	name := temp.Name()
	temp.Close()
	os.Remove(name)
	defer os.Remove(name)
	if b, e := os.ReadFile(indexPath); e == nil {
		if err = os.WriteFile(name, b, 0600); err != nil {
			return "", err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	if req.Action == "commit" {
		return r.commitIndex(ctx, req, state, name, lock)
	}
	if len(req.Paths) == 0 || len(req.Paths) > 1000 {
		return "", errors.New("select 1–1000 changed files")
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, path := range req.Paths {
		if err = ValidPath(path); err != nil {
			return "", err
		}
		var selected *GitFile
		for i := range state.Files {
			if state.Files[i].Path == path {
				selected = &state.Files[i]
				break
			}
		}
		if selected == nil || selected.Protected {
			return "", errors.New("select an exact, non-protected changed file")
		}
		if req.Action == "stage" && selected.Worktree == "" {
			return "", errors.New("file has no unstaged changes")
		}
		if req.Action == "unstage" && selected.Index == "" {
			return "", errors.New("file has no staged changes")
		}
		for _, p := range []string{selected.Path, selected.OldPath} {
			if p != "" && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	before := r.fingerprints(paths)
	if req.Action == "stage" {
		_, err = r.clientGit(ctx, name, append([]string{"add", "--all", "--"}, paths...)...)
	} else if state.Head == "" {
		_, err = r.clientGit(ctx, name, append([]string{"update-index", "--force-remove", "--"}, paths...)...)
	} else {
		_, err = r.clientGit(ctx, name, append([]string{"reset", "--quiet", state.Head, "--"}, paths...)...)
	}
	if err != nil {
		return "", err
	}
	// A file written to while it was being staged needs another look before it is staged.
	if r.fingerprints(paths) != before {
		return "", fmt.Errorf("a file changed while it was being staged; index was not replaced: %w", domain.ErrConflict)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	if err = lock.publish(b); err != nil {
		return "", err
	}
	verb, noun := "Staged", "files"
	if req.Action == "unstage" {
		verb = "Unstaged"
	}
	if len(req.Paths) == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%s %d %s", verb, len(req.Paths), noun), nil
}

var ErrGitUnknown = errors.New("Git effect may have completed")

type gitIndexLock struct {
	file      *os.File
	index     string
	published bool
}

func acquireGitIndex(path string) (*gitIndexLock, error) {
	file, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return nil, err
	}
	return &gitIndexLock{file: file, index: path}, nil
}
func (lock *gitIndexLock) release() {
	lock.file.Close()
	if !lock.published {
		_ = os.Remove(lock.file.Name())
	}
}
func (lock *gitIndexLock) publish(data []byte) error {
	if _, err := lock.file.Write(data); err != nil {
		return err
	}
	if err := lock.file.Sync(); err != nil {
		return err
	}
	if err := lock.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(lock.file.Name(), lock.index); err != nil {
		return err
	}
	lock.published = true // the lock path can now belong to a different Git process
	if err := syncDir(filepath.Dir(lock.index)); err != nil {
		return fmt.Errorf("%w: %v", ErrGitUnknown, err)
	}
	return nil
}
func (r *Repository) commitIndex(ctx context.Context, req GitRequest, state GitState, index string, lock *gitIndexLock) (string, error) {
	if state.Operation != "" {
		return "", errors.New("finish the current rebase before committing (Continue or Abort)")
	}
	if state.Detached {
		return "", errors.New("create and switch to a branch before committing from detached HEAD")
	}
	if strings.TrimSpace(req.Message) == "" || len(req.Message) > 10000 || strings.ContainsRune(req.Message, 0) {
		return "", errors.New("commit message must contain 1–10000 bytes")
	}
	staged := 0
	for _, file := range state.Files {
		if file.Conflict {
			return "", errors.New("resolve and stage conflicted files before committing")
		}
		if file.Index != "" {
			staged++
			if file.Protected {
				return "", errors.New("protected paths are staged; unstage them in Git before committing here")
			}
		}
	}
	if staged == 0 && req.All {
		// Nothing staged: commit every change, as VS Code does when asked.
		paths := []string{}
		for _, file := range state.Files {
			if !file.Protected {
				paths = append(paths, file.Path)
				if file.OldPath != "" {
					paths = append(paths, file.OldPath)
				}
			}
		}
		if len(paths) > 0 {
			before := r.fingerprints(paths)
			if _, err := r.clientGit(ctx, index, append([]string{"add", "--all", "--"}, paths...)...); err != nil {
				return "", err
			}
			if r.fingerprints(paths) != before {
				return "", fmt.Errorf("a file changed while it was being committed; nothing was committed: %w", domain.ErrConflict)
			}
			staged = len(paths)
		}
	}
	if req.Amend && state.Head == "" {
		return "", errors.New("there is no commit to amend yet")
	}
	if staged == 0 && !req.Amend {
		return "", errors.New("stage files before committing")
	}
	name, email := req.AuthorName, req.AuthorEmail
	if name == "" {
		name = state.AuthorName
	}
	if email == "" {
		email = state.AuthorEmail
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" || strings.ContainsAny(name+email, "\n\r\x00<>") || len(name) > 200 || len(email) > 320 {
		return "", errors.New("provide an author name and email for the commit")
	}
	tree, err := r.clientGit(ctx, index, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"-c", "user.name=" + name, "-c", "user.email=" + email, "-c", "commit.gpgSign=false", "commit-tree", strings.TrimSpace(tree)}
	var env []string
	if req.Amend {
		// The amended commit keeps the original's parents and author; the committer is whoever amends.
		line, e := r.clientGit(ctx, "", "rev-list", "--parents", "-n", "1", state.Head, "--")
		if e != nil {
			return "", e
		}
		for _, parent := range strings.Fields(line)[1:] {
			args = append(args, "-p", parent)
		}
		who, e := r.clientGit(ctx, "", "log", "-1", "--format=%an%x00%ae%x00%aI", state.Head, "--")
		if e != nil {
			return "", e
		}
		if p := strings.Split(strings.TrimSpace(who), "\x00"); len(p) == 3 {
			env = []string{"GIT_AUTHOR_NAME=" + p[0], "GIT_AUTHOR_EMAIL=" + p[1], "GIT_AUTHOR_DATE=" + p[2]}
		}
	} else if state.Head != "" {
		args = append(args, "-p", state.Head)
	}
	args = append(args, "-m", req.Message)
	hash, err := r.clientGitWith(ctx, false, index, env, args...)
	if err != nil {
		return "", err
	}
	hash = strings.TrimSpace(hash)
	// The index has been locked throughout, and update-ref below moves the branch only if it is still
	// where it was: nothing can change underneath the commit.
	old := state.Head
	if old == "" {
		old = strings.Repeat("0", len(hash))
	}
	reflog := "commit: "
	if req.Amend {
		reflog = "commit (amend): "
	}
	_, err = r.clientGit(ctx, "", "-c", "user.name="+name, "-c", "user.email="+email, "update-ref", "--create-reflog", "-m", reflog+strings.Split(req.Message, "\n")[0], "refs/heads/"+state.Branch, hash, old)
	if err != nil {
		return hash, err
	}
	if req.All && staged > 0 {
		// The commit took everything: the index now matches it.
		b, e := os.ReadFile(index)
		if e != nil {
			return hash, e
		}
		return hash, lock.publish(b)
	}
	return hash, nil
}

// Diff output is bounded before parsing/highlighting; Git still drains normally.
type boundedDiff struct {
	buffer    bytes.Buffer // Do not embed: promoted ReadFrom would bypass Write's limit.
	truncated bool
}

func (b *boundedDiff) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 512 << 10
	if b.buffer.Len()+n > limit {
		b.truncated = true
	}
	// Retain a complete boundary rune so Prefix can cut without splitting it.
	remaining := limit + utf8.UTFMax - b.buffer.Len()
	if len(p) > remaining {
		p = p[:max(0, remaining)]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
func (r *Repository) ClientDiff(ctx context.Context, path, area, commit string) (string, bool, error) {
	return r.ClientDiffContext(ctx, path, area, commit, 3)
}

// ClientDiffContext is ClientDiff with a number of context lines (a whole file: a very large number).
func (r *Repository) ClientDiffContext(ctx context.Context, path, area, commit string, context int) (string, bool, error) {
	if err := ValidPath(path); err != nil {
		return "", false, err
	}
	if context < 0 || context > 1000000 {
		context = 3
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=" + strconv.Itoa(context)}
	noIndex := false
	switch area {
	case "staged":
		args = append(args, "--cached")
	case "working":
		tracked, err := r.clientGit(ctx, "", "ls-files", "--", path)
		if err != nil {
			return "", false, err
		}
		if unmerged, _ := r.clientGit(ctx, "", "ls-files", "--unmerged", "--", path); unmerged != "" {
			// A conflicted file has no single staged version: show it against HEAD, conflict markers and all.
			args = append(args, "HEAD")
		} else if tracked == "" {
			abs, err := r.securePath(path)
			if err != nil {
				return "", false, err
			}
			info, err := os.Stat(abs)
			if err != nil {
				return "", false, err
			}
			if !info.Mode().IsRegular() {
				return "", false, errors.New("not a regular source file")
			}
			// Relative to the repository, so the diff names it the same way on every system.
			args = append(args, "--no-index", "--", "/dev/null", path)
			noIndex = true
		}
	case "commit":
		if !ValidCommit(commit) {
			return "", false, errors.New("a full commit hash is required")
		}
		parent, err := r.commitParent(ctx, commit)
		if err != nil {
			return "", false, err
		}
		args = append(args, parent, commit)
	default:
		return "", false, errors.New("diff area must be working, staged or commit")
	}
	if !noIndex {
		args = append(args, "--", path)
	}
	cmd, err := r.gitCommand(ctx, args...)
	if err != nil {
		return "", false, err
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1")
	var out boundedDiff
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil {
		var code *exec.ExitError
		if !noIndex || !errors.As(err, &code) || code.ExitCode() != 1 {
			return "", false, fmt.Errorf("git diff: %s: %w", stderr.String(), err)
		}
	}
	return domain.Prefix(out.buffer.String(), 512<<10), out.truncated, nil
}
func (r *Repository) commitParent(ctx context.Context, hash string) (string, error) {
	if !ValidCommit(hash) {
		return "", errors.New("a full commit hash is required")
	}
	line, err := r.clientGit(ctx, "", "rev-list", "--parents", "-n", "1", hash, "--")
	if err != nil {
		return "", err
	}
	ids := strings.Fields(line)
	if len(ids) > 1 {
		return ids[1], nil
	}
	empty, err := r.clientGit(ctx, "", "hash-object", "-t", "tree", sysenv.Null)
	return strings.TrimSpace(empty), err
}
