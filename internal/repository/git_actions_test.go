package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole VS Code round with a remote that is a folder (offline): publish, a commit arriving from
// elsewhere, fetch and pull, push, amend, undo, branches and a conflicted merge, stashes, discards.
func TestSourceControlRoundTripWithLocalRemote(t *testing.T) {
	// The owner's global git config must not leak into the test.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := fixture(t)
	ctx := context.Background()
	sh := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Other", "GIT_AUTHOR_EMAIL=o@x.test", "GIT_COMMITTER_NAME=Other", "GIT_COMMITTER_EMAIL=o@x.test")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, b)
		}
		return string(b)
	}
	state := func() GitState {
		t.Helper()
		s, err := r.GitState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	do := func(req GitRequest) string {
		t.Helper()
		req.Revision = state().Revision
		out, err := r.ApplyGit(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Action, err)
		}
		return out
	}
	fails := func(req GitRequest, want string) {
		t.Helper()
		req.Revision = state().Revision
		if _, err := r.ApplyGit(ctx, req); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want error with %q, got %v", req.Action, want, err)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Path, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fails(GitRequest{Action: "push"}, "no remote")
	bare := filepath.Join(t.TempDir(), "remote.git")
	sh(filepath.Dir(bare), "init", "--bare", "-b", "main", bare)
	do(GitRequest{Action: "remote.add", Remote: "origin", URL: bare})
	if out := do(GitRequest{Action: "publish"}); !strings.Contains(out, "Published main") {
		t.Fatal(out)
	}
	if s := state(); s.Upstream != "origin/main" || s.Ahead != 0 || !s.HeadPushed {
		t.Fatalf("after publish: %+v", s)
	}

	// Someone else pushes; fetch sees it, pull brings it.
	other := filepath.Join(t.TempDir(), "other")
	sh(filepath.Dir(other), "clone", "-q", bare, other)
	os.WriteFile(filepath.Join(other, "b.txt"), []byte("from elsewhere\n"), 0o644)
	sh(other, "add", ".")
	sh(other, "-c", "commit.gpgSign=false", "commit", "-qm", "theirs")
	sh(other, "push", "-q")
	do(GitRequest{Action: "fetch"})
	if s := state(); s.Behind != 1 {
		t.Fatalf("behind after fetch: %d", s.Behind)
	}
	if out := do(GitRequest{Action: "pull"}); !strings.Contains(out, "Pulled 1") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "b.txt")); err != nil {
		t.Fatal("pull did not bring b.txt")
	}

	// Commit everything (nothing staged), amend it, push it.
	write("a.py", "print('mine')\n")
	write("new.txt", "new\n")
	do(GitRequest{Action: "commit", All: true, Message: "mine"})
	if s := state(); !s.Clean || s.Ahead != 1 || s.LastMessage != "mine" {
		t.Fatalf("after commit all: clean=%v ahead=%d files=%+v", s.Clean, s.Ahead, s.Files)
	}
	do(GitRequest{Action: "commit", Amend: true, Message: "mine, amended"})
	if s := state(); s.LastMessage != "mine, amended" || s.Ahead != 1 {
		t.Fatalf("after amend: %+v", s.LastMessage)
	}
	if out := do(GitRequest{Action: "push"}); !strings.Contains(out, "Pushed 1") {
		t.Fatal(out)
	}

	// Undo the last commit: its changes come back staged.
	write("c.txt", "c\n")
	do(GitRequest{Action: "stage", Paths: []string{"c.txt"}})
	do(GitRequest{Action: "commit", Message: "add c"})
	do(GitRequest{Action: "undo"})
	if s := state(); len(s.Files) != 1 || s.Files[0].Path != "c.txt" || s.Files[0].Index != "A" {
		t.Fatalf("after undo: %+v", s.Files)
	}
	do(GitRequest{Action: "unstage.all"})
	if s := state(); s.Files[0].Index != "" {
		t.Fatalf("unstage all: %+v", s.Files)
	}

	// Discard: an untracked file goes, a tracked change reverts.
	write("a.py", "scribble\n")
	do(GitRequest{Action: "discard.all"})
	if s := state(); !s.Clean {
		t.Fatalf("discard all left %+v", s.Files)
	}
	// Line endings follow the machine's git settings (CRLF on Windows by default).
	if b := lf(filepath.Join(r.Path, "a.py")); string(b) != "print('mine')\n" {
		t.Fatalf("a.py after discard: %q", b)
	}

	// Stash and pop.
	write("a.py", "stashed\n")
	do(GitRequest{Action: "stash.save", Message: "wip"})
	s := state()
	if !s.Clean || len(s.Stashes) != 1 || !strings.Contains(s.Stashes[0].Message, "wip") {
		t.Fatalf("stash: %+v", s)
	}
	do(GitRequest{Action: "stash.pop", Stash: s.Stashes[0].Ref})
	if b := lf(filepath.Join(r.Path, "a.py")); string(b) != "stashed\n" {
		t.Fatal("pop did not restore")
	}
	do(GitRequest{Action: "discard", Paths: []string{"a.py"}})

	// A branch whose change conflicts with main; merge it, take ours, commit the merge.
	do(GitRequest{Action: "branch.create", Branch: "feature"})
	do(GitRequest{Action: "branch.switch", Branch: "feature"})
	write("a.py", "feature\n")
	do(GitRequest{Action: "commit", All: true, Message: "feature change"})
	do(GitRequest{Action: "branch.switch", Branch: "main"})
	write("a.py", "main\n")
	do(GitRequest{Action: "commit", All: true, Message: "main change"})
	if out := do(GitRequest{Action: "merge", Branch: "feature"}); !strings.Contains(out, "1 conflicted") {
		t.Fatal(out)
	}
	s = state()
	if s.Operation != "MERGE_HEAD" || !strings.Contains(s.MergeMessage, "feature") {
		t.Fatalf("merge state: %q %q", s.Operation, s.MergeMessage)
	}
	fails(GitRequest{Action: "commit", Message: "x"}, "conflicted")
	do(GitRequest{Action: "conflict.ours", Paths: []string{"a.py"}})
	do(GitRequest{Action: "commit"})
	if s := state(); s.Operation != "" || !s.Clean {
		t.Fatalf("after merge commit: %+v", s)
	}
	if b := lf(filepath.Join(r.Path, "a.py")); string(b) != "main\n" {
		t.Fatalf("ours: %q", b)
	}

	// An aborted merge leaves things as they were.
	do(GitRequest{Action: "branch.switch", Branch: "feature"})
	write("a.py", "feature again\n")
	do(GitRequest{Action: "commit", All: true, Message: "again"})
	do(GitRequest{Action: "branch.switch", Branch: "main"})
	write("a.py", "main again\n")
	do(GitRequest{Action: "commit", All: true, Message: "main again"})
	do(GitRequest{Action: "merge", Branch: "feature"})
	do(GitRequest{Action: "operation.abort"})
	if s := state(); s.Operation != "" || !s.Clean {
		t.Fatalf("after abort: %+v", s)
	}

	// Branches: an unmerged one asks for force; rename; a remote branch checked out.
	fails(GitRequest{Action: "branch.delete", Branch: "feature"}, "not merged")
	do(GitRequest{Action: "branch.rename", Branch: "feature", NewName: "feature2"})
	do(GitRequest{Action: "branch.delete", Branch: "feature2", Force: true})
	sh(other, "switch", "-qc", "topic")
	sh(other, "push", "-q", "-u", "origin", "topic")
	do(GitRequest{Action: "fetch"})
	do(GitRequest{Action: "branch.checkout", Branch: "origin/topic"})
	if s := state(); s.Branch != "topic" || s.Upstream != "origin/topic" {
		t.Fatalf("checkout remote: %s %s", s.Branch, s.Upstream)
	}
	do(GitRequest{Action: "branch.switch", Branch: "main"})

	// Sync: pull what arrived, push what is ours.
	sh(other, "switch", "-q", "main")
	sh(other, "pull", "-q", "--no-rebase")
	os.WriteFile(filepath.Join(other, "d.txt"), []byte("d\n"), 0o644)
	sh(other, "add", ".")
	sh(other, "-c", "commit.gpgSign=false", "commit", "-qm", "d")
	sh(other, "push", "-q")
	if out := do(GitRequest{Action: "sync"}); !strings.Contains(out, "Pulled") || !strings.Contains(out, "pushed") {
		t.Fatal(out)
	}
	if s := state(); s.Ahead != 0 || s.Behind != 0 {
		t.Fatalf("after sync: ahead %d behind %d", s.Ahead, s.Behind)
	}
}

func TestRemoteURLsHideSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@github.com/a/b.git":                 "https://user:•••@github.com/a/b.git",
		"https://ghp_aaaaaaaaaaaaaaaaaaaaaaaa@github.com/a/b": "https://•••@github.com/a/b",
		"git@github.com:a/b.git":                              "git@github.com:a/b.git",
		"https://github.com/a/b":                              "https://github.com/a/b",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// lf reads a file with its line endings as LF: git writes files with the machine's line endings (CRLF on
// Windows by default), and these tests compare text.
func lf(path string) string {
	b, _ := os.ReadFile(path)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
