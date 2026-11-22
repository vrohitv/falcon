package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The rest of what VS Code's Source Control does: discard, undo the last commit, branches (delete, rename,
// check out from a remote, merge), stashes, conflicts, and the remote (fetch, pull, push, sync, publish).
// Each runs under the repository's lock after ApplyGit's revision check, so it acts on what the owner saw.

var (
	stashRef   = regexp.MustCompile(`^stash@\{[0-9]{1,4}\}$`)
	remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// NetworkAction reports whether an action talks to a remote (it gets longer to finish).
func NetworkAction(action string) bool {
	switch action {
	case "fetch", "pull", "push", "sync", "publish":
		return true
	}
	return false
}

func inProgress(op string) bool {
	return op == "MERGE_HEAD" || op == "CHERRY_PICK_HEAD" || op == "REVERT_HEAD"
}

func (r *Repository) applyMore(ctx context.Context, req GitRequest, state GitState) (string, error) {
	busy := func() error {
		if state.Operation != "" {
			return errors.New("finish or abort the merge in progress first")
		}
		return nil
	}
	switch req.Action {
	case "discard", "discard.all":
		return r.discard(ctx, req, state)
	case "undo":
		if err := busy(); err != nil {
			return "", err
		}
		if state.Detached || state.Head == "" {
			return "", errors.New("there is no commit on this branch to undo")
		}
		line, err := r.clientGit(ctx, "", "rev-list", "--parents", "-n", "1", state.Head, "--")
		if err != nil {
			return "", err
		}
		parents := strings.Fields(line)[1:]
		if len(parents) == 0 {
			return "", errors.New("the first commit cannot be undone here")
		}
		if len(parents) > 1 {
			return "", errors.New("the last commit is a merge; undo it from a terminal (git reset)")
		}
		subject, _ := r.clientGit(ctx, "", "log", "-1", "--format=%s", state.Head, "--")
		// Like git reset --soft HEAD~: the branch steps back and the commit's changes stay staged.
		if _, err = r.clientGit(ctx, "", "update-ref", "--create-reflog", "-m", "falcon: undo last commit", "refs/heads/"+state.Branch, parents[0], state.Head); err != nil {
			return "", err
		}
		return "Undid “" + strings.TrimSpace(subject) + "”; its changes are staged", nil
	case "branch.delete", "branch.rename":
		if err := r.branchName(ctx, req.Branch); err != nil {
			return "", err
		}
		var found *GitBranch
		for i := range state.Branches {
			if state.Branches[i].Name == req.Branch {
				found = &state.Branches[i]
			}
		}
		if found == nil {
			return "", errors.New("local branch not found")
		}
		if req.Action == "branch.rename" {
			if err := r.branchName(ctx, req.NewName); err != nil {
				return "", err
			}
			_, err := r.clientGit(ctx, "", "branch", "-m", "--", req.Branch, req.NewName)
			return req.NewName, err
		}
		if found.Current {
			return "", errors.New("switch to another branch before deleting this one")
		}
		if found.Worktree != "" {
			return "", errors.New("branch is checked out in another worktree")
		}
		flag := "-d"
		if req.Force {
			flag = "-D"
		}
		out, err := r.clientGit(ctx, "", "branch", flag, "--", req.Branch)
		if err != nil && strings.Contains(out, "not fully merged") {
			return "", errors.New(req.Branch + " has commits not merged anywhere; delete it anyway to lose them")
		}
		return "Deleted " + req.Branch, err
	case "branch.checkout":
		// A remote branch (origin/feature) checked out as a local one that tracks it.
		if err := busy(); err != nil {
			return "", err
		}
		known := false
		for _, b := range state.RemoteBranches {
			known = known || b == req.Branch
		}
		if !known {
			return "", errors.New("remote branch not found; fetch first")
		}
		local := req.Branch[strings.Index(req.Branch, "/")+1:]
		if err := r.branchName(ctx, local); err != nil {
			return "", err
		}
		for _, b := range state.Branches {
			if b.Name == local {
				return "", fmt.Errorf("a local branch %s exists; switch to it", local)
			}
		}
		if err := r.protectedChange(ctx, state.Head, "refs/remotes/"+req.Branch); err != nil {
			return "", err
		}
		if _, err := r.clientGit(ctx, "", "switch", "--no-overwrite-ignore", "-c", local, "--track", "refs/remotes/"+req.Branch); err != nil {
			return "", friendlySwitch(err)
		}
		return local, nil
	case "merge":
		if err := busy(); err != nil {
			return "", err
		}
		if state.Detached {
			return "", errors.New("switch to a branch before merging into it")
		}
		ref := ""
		for _, b := range state.Branches {
			if b.Name == req.Branch && !b.Current {
				ref = "refs/heads/" + b.Name
			}
		}
		for _, b := range state.RemoteBranches {
			if b == req.Branch {
				ref = "refs/remotes/" + b
			}
		}
		if ref == "" {
			return "", errors.New("choose another branch to merge")
		}
		return r.merge(ctx, state, ref, req.Branch)
	case "operation.abort":
		var args []string
		switch {
		case state.Operation == "MERGE_HEAD":
			args = []string{"merge", "--abort"}
		case strings.HasPrefix(state.Operation, "rebase"):
			args = []string{"rebase", "--abort"}
		case state.Operation == "CHERRY_PICK_HEAD":
			args = []string{"cherry-pick", "--abort"}
		case state.Operation == "REVERT_HEAD":
			args = []string{"revert", "--abort"}
		default:
			return "", errors.New("no merge, rebase, cherry-pick or revert is in progress")
		}
		_, err := r.clientGit(ctx, "", args...)
		return "Aborted the " + strings.Split(args[0], "-")[0], err
	case "operation.continue":
		if !strings.HasPrefix(state.Operation, "rebase") {
			return r.commitOperation(ctx, req, state)
		}
		if conflicted(state) > 0 {
			return "", errors.New("resolve and stage the conflicted files first")
		}
		name, email := r.identity(ctx)
		_, err := r.ownerGit(ctx, []string{"GIT_EDITOR=true"}, "-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgSign=false", "rebase", "--continue")
		if err != nil && r.operation(ctx) != "" {
			return "Rebase stopped again with conflicts: resolve them, then continue", nil
		}
		return "Rebase continued", err
	case "conflict.ours", "conflict.theirs":
		paths, err := r.conflictPaths(req, state)
		if err != nil {
			return "", err
		}
		side := "--" + strings.TrimPrefix(req.Action, "conflict.")
		if _, err = r.clientGit(ctx, "", append([]string{"checkout", side, "--"}, paths...)...); err != nil {
			return "", err
		}
		_, err = r.clientGit(ctx, "", append([]string{"add", "--"}, paths...)...)
		return fmt.Sprintf("Took %s side for %d file(s)", strings.TrimPrefix(side, "--"), len(paths)), err
	case "stash.save":
		if err := busy(); err != nil {
			return "", err
		}
		if state.Clean {
			return "", errors.New("there are no changes to stash")
		}
		name, email := r.identity(ctx)
		args := []string{"-c", "user.name=" + fallback(name, "Falcon"), "-c", "user.email=" + fallback(email, "falcon@localhost"), "stash", "push"}
		if req.IncludeUntracked {
			args = append(args, "--include-untracked")
		}
		if m := strings.TrimSpace(req.Message); m != "" {
			args = append(args, "-m", m)
		}
		_, err := r.clientGit(ctx, "", args...)
		return "Stashed changes", err
	case "stash.apply", "stash.pop", "stash.drop":
		if !stashRef.MatchString(req.Stash) {
			return "", errors.New("choose a stash")
		}
		known := false
		for _, st := range state.Stashes {
			known = known || st.Ref == req.Stash
		}
		if !known {
			return "", errors.New("stash not found")
		}
		verb := strings.TrimPrefix(req.Action, "stash.")
		out, err := r.clientGit(ctx, "", "stash", verb, req.Stash)
		if err != nil && strings.Contains(out, "CONFLICT") {
			return "Applied with conflicts: resolve them; the stash was kept", nil
		}
		return map[string]string{"apply": "Applied ", "pop": "Popped ", "drop": "Dropped "}[verb] + req.Stash, err
	case "remote.add":
		if !remoteName.MatchString(req.Remote) {
			return "", errors.New("name the remote with letters, digits, dots, dashes or underscores")
		}
		u := strings.TrimSpace(req.URL)
		if u == "" || strings.HasPrefix(u, "-") || strings.ContainsAny(u, " \t\r\n\x00") || strings.HasPrefix(strings.ToLower(u), "ext::") {
			return "", errors.New("enter the remote's URL (https://…, git@host:…, or a folder)")
		}
		_, err := r.clientGit(ctx, "", "remote", "add", "--", req.Remote, u)
		return "Added remote " + req.Remote, err
	case "fetch", "pull", "push", "sync", "publish":
		return r.remote(ctx, req, state)
	}
	return "", errors.New("unsupported owner Git action")
}

func fallback(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func (r *Repository) branchName(ctx context.Context, name string) error {
	if name == "" || strings.HasPrefix(name, "-") || managedBranch(name) || strings.HasPrefix(name, "refs/") {
		return errors.New("enter a local branch name")
	}
	if _, err := r.clientGit(ctx, "", "check-ref-format", "--branch", name); err != nil {
		return errors.New("invalid branch name")
	}
	return nil
}

// protectedChange refuses to bring changes to protected paths (.env, .git, node_modules…) into the
// working tree, as a branch switch already does.
func (r *Repository) protectedChange(ctx context.Context, from, to string) error {
	if from == "" {
		return nil
	}
	changed, err := r.clientGit(ctx, "", "diff", "--name-only", "-z", from, to, "--")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(changed, "\x00") {
		if path != "" && Protected(path) {
			return fmt.Errorf("this would change a protected path (%s); do it from a terminal", path)
		}
	}
	return nil
}

func friendlySwitch(err error) error {
	if strings.Contains(err.Error(), "would be overwritten") {
		return errors.New("your changes to some files would be overwritten by the other branch: commit or stash them first (nothing was changed)")
	}
	return err
}

func conflicted(state GitState) int {
	n := 0
	for _, f := range state.Files {
		if f.Conflict {
			n++
		}
	}
	return n
}

func (r *Repository) conflictPaths(req GitRequest, state GitState) ([]string, error) {
	if len(req.Paths) == 0 {
		return nil, errors.New("choose a conflicted file")
	}
	for _, p := range req.Paths {
		ok := false
		for _, f := range state.Files {
			ok = ok || (f.Path == p && f.Conflict && !f.Protected)
		}
		if !ok {
			return nil, fmt.Errorf("%s is not a conflicted file", p)
		}
	}
	return req.Paths, nil
}

func (r *Repository) operation(ctx context.Context) string {
	_, dir := r.gitDirs(ctx)
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
	}
	return ""
}

// merge merges ref into the current branch. Conflicts are not a failure: the merge waits for them to be
// resolved and committed (or aborted).
func (r *Repository) merge(ctx context.Context, state GitState, ref, label string) (string, error) {
	if err := r.protectedChange(ctx, state.Head, ref); err != nil {
		return "", err
	}
	name, email := r.identity(ctx)
	if name == "" || email == "" {
		return "", errors.New("set your name and email (git config user.name / user.email) before merging")
	}
	out, err := r.ownerGit(ctx, nil, "-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgSign=false", "merge", "--no-edit", ref)
	if err != nil {
		if r.operation(ctx) == "MERGE_HEAD" {
			after, _ := r.gitState(ctx)
			return fmt.Sprintf("Merging %s stopped with %d conflicted file(s): resolve them, then commit", label, conflicted(after)), nil
		}
		if strings.Contains(out, "would be overwritten") {
			return "", errors.New("your uncommitted changes would be overwritten by the merge: commit or stash them first (nothing was changed)")
		}
		return "", err
	}
	if strings.Contains(out, "Already up to date") {
		return "Already up to date with " + label, nil
	}
	if strings.Contains(out, "Fast-forward") {
		return "Fast-forwarded to " + label, nil
	}
	return "Merged " + label, nil
}

// commitOperation finishes a merge, cherry-pick or revert with a commit from the real index.
func (r *Repository) commitOperation(ctx context.Context, req GitRequest, state GitState) (string, error) {
	if n := conflicted(state); n > 0 {
		return "", fmt.Errorf("resolve and stage the %d conflicted file(s) first", n)
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		msg = strings.TrimSpace(state.MergeMessage)
	}
	if msg == "" || len(msg) > 10000 || strings.ContainsRune(msg, 0) {
		return "", errors.New("write a commit message")
	}
	name, email := req.AuthorName, req.AuthorEmail
	if name == "" || email == "" {
		name, email = r.identity(ctx)
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		return "", errors.New("provide an author name and email for the commit")
	}
	if _, err := r.clientGit(ctx, "", "-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgSign=false", "commit", "--no-verify", "-m", msg); err != nil {
		return "", err
	}
	head, err := r.clientGit(ctx, "", "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

// discard throws away unstaged changes: tracked files go back to what is staged (or committed), new
// files are deleted. The panel confirms first; it cannot be undone.
func (r *Repository) discard(ctx context.Context, req GitRequest, state GitState) (string, error) {
	want := map[string]bool{}
	for _, p := range req.Paths {
		want[p] = true
	}
	restore, remove := []string{}, []string{}
	for _, f := range state.Files {
		if f.Protected || f.Conflict || f.Worktree == "" || (req.Action == "discard" && !want[f.Path]) {
			continue
		}
		delete(want, f.Path)
		if f.Worktree == "?" {
			remove = append(remove, f.Path)
		} else {
			restore = append(restore, f.Path)
		}
	}
	if req.Action == "discard" && len(want) > 0 {
		for p := range want {
			return "", fmt.Errorf("%s has no unstaged changes to discard", p)
		}
	}
	if len(restore)+len(remove) == 0 {
		return "", errors.New("there are no unstaged changes to discard")
	}
	if len(restore) > 0 {
		// Back to what is staged; git also refreshes what it knows of the files, so they show as unchanged.
		if _, err := r.clientGit(ctx, "", append([]string{"restore", "--worktree", "--"}, restore...)...); err != nil {
			if _, err2 := r.clientGit(ctx, "", append([]string{"checkout-index", "--force", "--"}, restore...)...); err2 != nil {
				return "", err
			}
		}
	}
	if len(remove) > 0 {
		root, err := os.OpenRoot(r.Path)
		if err != nil {
			return "", err
		}
		defer root.Close()
		for _, p := range remove {
			if err := root.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
	n := len(restore) + len(remove)
	if n == 1 {
		return "Discarded changes to 1 file", nil
	}
	return fmt.Sprintf("Discarded changes to %d files", n), nil
}

// remote fetches, pulls, pushes, syncs (pull then push) or publishes (pushes a branch that has no upstream
// and tracks it), with the owner's credentials.
func (r *Repository) remote(ctx context.Context, req GitRequest, state GitState) (string, error) {
	if len(state.Remotes) == 0 {
		return "", errors.New("this repository has no remote: add one first")
	}
	if req.Action == "fetch" {
		if _, err := r.ownerGit(ctx, nil, "fetch", "--all", "--prune"); err != nil {
			return "", remoteError(err)
		}
		after, _ := r.gitState(ctx)
		if after.Upstream != "" && after.Behind > 0 {
			return fmt.Sprintf("Fetched: %d commit(s) to pull", after.Behind), nil
		}
		return "Fetched", nil
	}
	if state.Detached {
		return "", errors.New("switch to a branch first")
	}
	if state.Operation != "" {
		return "", errors.New("finish or abort the merge in progress first")
	}
	remoteFor := func() string {
		if req.Remote != "" {
			return req.Remote
		}
		for _, rm := range state.Remotes {
			if rm.Name == "origin" {
				return "origin"
			}
		}
		return state.Remotes[0].Name
	}
	if req.Action == "publish" || (req.Action == "push" && state.Upstream == "") || (req.Action == "sync" && state.Upstream == "") {
		remote := remoteFor()
		known := false
		for _, rm := range state.Remotes {
			known = known || rm.Name == remote
		}
		if !known {
			return "", errors.New("remote not found")
		}
		if _, err := r.ownerGit(ctx, nil, "push", "--set-upstream", "--", remote, "refs/heads/"+state.Branch+":refs/heads/"+state.Branch); err != nil {
			return "", remoteError(err)
		}
		return "Published " + state.Branch + " to " + remote, nil
	}
	pulled := ""
	if req.Action == "pull" || req.Action == "sync" {
		remote := strings.SplitN(state.Upstream, "/", 2)[0]
		if _, err := r.ownerGit(ctx, nil, "fetch", "--prune", "--", remote); err != nil {
			return "", remoteError(err)
		}
		after, err := r.gitState(ctx)
		if err != nil {
			return "", err
		}
		if after.Behind > 0 {
			rebase, _ := r.ownerGit(ctx, nil, "config", "--get", "pull.rebase")
			if strings.TrimSpace(rebase) == "true" {
				if err := r.protectedChange(ctx, after.Head, "@{upstream}"); err != nil {
					return "", err
				}
				name, email := r.identity(ctx)
				if _, err := r.ownerGit(ctx, []string{"GIT_EDITOR=true"}, "-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgSign=false", "rebase", "--autostash", "@{upstream}"); err != nil {
					if r.operation(ctx) != "" {
						return "Pull stopped with conflicts while rebasing: resolve them, then continue", nil
					}
					return "", err
				}
				pulled = fmt.Sprintf("Pulled %d commit(s)", after.Behind)
			} else {
				msg, err := r.merge(ctx, after, "@{upstream}", after.Upstream)
				if err != nil || strings.Contains(msg, "conflicted") {
					return msg, err
				}
				pulled = fmt.Sprintf("Pulled %d commit(s)", after.Behind)
			}
		} else {
			pulled = "Already up to date"
		}
		if req.Action == "pull" {
			return pulled, nil
		}
		state, err = r.gitState(ctx)
		if err != nil {
			return "", err
		}
	}
	if state.Ahead == 0 {
		if pulled != "" {
			return pulled + "; nothing to push", nil
		}
		return "Nothing to push", nil
	}
	remote := strings.SplitN(state.Upstream, "/", 2)[0]
	dest := strings.TrimPrefix(state.Upstream, remote+"/")
	args := []string{"push"}
	if req.Force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, "--", remote, "refs/heads/"+state.Branch+":refs/heads/"+dest)
	out, err := r.ownerGit(ctx, nil, args...)
	if err != nil {
		if strings.Contains(out, "rejected") || strings.Contains(out, "fetch first") || strings.Contains(out, "non-fast-forward") {
			return "", errors.New("the remote has commits you don't have: pull (or sync) first")
		}
		return "", remoteError(err)
	}
	msg := fmt.Sprintf("Pushed %d commit(s) to %s", state.Ahead, state.Upstream)
	if pulled != "" && pulled != "Already up to date" {
		msg = pulled + " and pushed " + strconv.Itoa(state.Ahead)
	}
	return msg, nil
}

// remoteError says what to do about the usual ways reaching a remote fails.
func remoteError(err error) error {
	m := err.Error()
	switch {
	case strings.Contains(m, "Could not resolve host") || strings.Contains(m, "Network is unreachable") || strings.Contains(m, "Operation timed out") || strings.Contains(m, "Connection timed out"):
		return errors.New("can't reach the remote (offline?): everything else works without it; try again when connected")
	case strings.Contains(m, "terminal prompts disabled") || strings.Contains(m, "Authentication failed") || strings.Contains(m, "could not read Username"):
		return errors.New("the remote wants credentials: set up a credential helper (e.g. gh auth setup-git) or an SSH key, then retry")
	case strings.Contains(m, "Permission denied (publickey)") || strings.Contains(m, "Host key verification failed"):
		return errors.New("SSH refused: add your key to the agent (ssh-add) and connect once from a terminal to trust the host")
	}
	return err
}

// identity is who commits: the repository's user.name/user.email, else the owner's global ones.
func (r *Repository) identity(ctx context.Context) (string, string) {
	cfg := r.settings(ctx)
	return cfg.name, cfg.email
}

type gitSettings struct {
	name, email string
	remotes     []GitRemote
}

// settings are the author and the remotes, read in one git call and kept until a configuration file
// changes (by size or time). On Windows each git process costs tens of milliseconds, so the panel's
// refresh asks as few questions as it can.
func (r *Repository) settings(ctx context.Context) gitSettings {
	common, dir := r.gitDirs(ctx)
	home, _ := os.UserHomeDir()
	if h := os.Getenv("HOME"); h != "" {
		home = h
	}
	files := []string{filepath.Join(common, "config"), filepath.Join(dir, "config.worktree"), filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git", "config")}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		files = append(files, filepath.Join(x, "git", "config"))
	}
	var key strings.Builder
	for _, name := range files {
		if info, err := os.Stat(name); err == nil {
			fmt.Fprintf(&key, "%s:%d:%d;", name, info.Size(), info.ModTime().UnixNano())
		}
	}
	f := &r.filters
	f.Lock()
	if f.settingsKey == key.String() && time.Since(f.settingsAt) < time.Hour {
		cfg := f.settings
		f.Unlock()
		return cfg
	}
	f.Unlock()
	cfg := gitSettings{remotes: []GitRemote{}}
	// The owner's configuration: global first, then the repository's, so the repository's wins.
	out, _ := r.ownerGit(ctx, nil, "config", "-z", "--get-regexp", `^(user\.(name|email)|remote\..+\.url)$`)
	seen := map[string]bool{}
	for _, kv := range strings.Split(out, "\x00") {
		k, v, ok := strings.Cut(kv, "\n")
		if !ok {
			continue
		}
		switch {
		case k == "user.name":
			cfg.name = strings.TrimSpace(v)
		case k == "user.email":
			cfg.email = strings.TrimSpace(v)
		case strings.HasPrefix(k, "remote.") && strings.HasSuffix(k, ".url"):
			name := strings.TrimSuffix(strings.TrimPrefix(k, "remote."), ".url")
			if !seen[name] {
				seen[name] = true
				cfg.remotes = append(cfg.remotes, GitRemote{Name: name, URL: redactURL(v)})
			}
		}
	}
	sort.Slice(cfg.remotes, func(i, j int) bool { return cfg.remotes[i].Name < cfg.remotes[j].Name })
	f.Lock()
	f.settings, f.settingsKey, f.settingsAt = cfg, key.String(), time.Now()
	f.Unlock()
	return cfg
}

// gitDirs are the repository's common git folder and this worktree's own (see GitDirs).
func (r *Repository) gitDirs(context.Context) (string, string) { return r.GitDirs() }

// redactURL drops a password or token written into a remote's URL before it is shown.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	name := u.User.Username()
	if _, has := u.User.Password(); has {
		name += ":•••"
	} else if strings.HasPrefix(u.Scheme, "http") && len(name) > 20 {
		name = "•••" // a token used as the user name
	}
	u.User = nil
	plain := u.String()
	i := strings.Index(plain, "://")
	return plain[:i+3] + name + "@" + plain[i+3:]
}
