package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGitDiffTruncationPreservesUTF8(t *testing.T) {
	r := fixture(t)
	for _, prefix := range []string{"", "x"} {
		if err := os.WriteFile(filepath.Join(r.Path, "unicode.txt"), []byte(prefix+strings.Repeat("é", 300000)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		diff, truncated, err := r.ClientDiff(context.Background(), "unicode.txt", "working", "")
		if err != nil || !truncated || !utf8.ValidString(diff) || len(diff) > 512<<10 {
			t.Fatalf("invalid bounded Unicode diff: truncated=%t valid=%t bytes=%d err=%v", truncated, utf8.ValidString(diff), len(diff), err)
		}
	}
}

func TestGitIndexReleaseKeepsSuccessorLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	lock, err := acquireGitIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = lock.publish([]byte("published")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".lock", []byte("successor"), 0600); err != nil {
		t.Fatal(err)
	}
	lock.release()
	got, err := os.ReadFile(path + ".lock")
	if err != nil || string(got) != "successor" {
		t.Fatalf("removed another client's index lock: %q %v", got, err)
	}
}

func TestGitCommitUsesStagedContentAndHistoryReadsLinkedWorktree(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	apply := func(action string, paths []string) {
		t.Helper()
		state, err := r.GitState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.ApplyGit(ctx, GitRequest{Action: action, Revision: state.Revision, Paths: paths, Message: "staged version"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(r.Path, "a.py"), []byte("staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	apply("stage", []string{"a.py"})
	if err := os.WriteFile(filepath.Join(r.Path, "a.py"), []byte("unstaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	apply("commit", nil)
	content, err := r.Git(ctx, "show", "HEAD:a.py")
	if err != nil || content != "staged\n" {
		t.Fatalf("committed working copy: %q %v", content, err)
	}
	state, err := r.GitState(ctx)
	if err != nil || len(state.Files) != 1 || state.Files[0].Worktree != "M" || state.Files[0].Index != "" {
		t.Fatalf("unstaged changes lost: %+v %v", state, err)
	}
	worktree := filepath.Join(t.TempDir(), "linked")
	if _, err = r.Git(ctx, "worktree", "add", "-b", "linked", worktree, "HEAD"); err != nil {
		t.Fatal(err)
	}
	linked, err := Open(worktree)
	if err != nil {
		t.Fatal(err)
	}
	log, err := linked.CommitLog(ctx, "", 0, 10)
	if err != nil || log.Backend != "go-git" || len(log.Commits) != 2 || log.Commits[0].Subject != "staged version" {
		t.Fatalf("linked history: %+v %v", log, err)
	}
	if _, err = r.Git(ctx, "gc", "--quiet"); err != nil {
		t.Fatal(err)
	}
	log, err = linked.CommitLog(ctx, "", 0, 10)
	if err != nil || log.Backend != "go-git" || len(log.Commits) != 2 {
		t.Fatalf("packed history: %+v %v", log, err)
	}
}

func TestGitRenameDeleteAndBinaryDiff(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	if err := os.Rename(filepath.Join(r.Path, "a.py"), filepath.Join(r.Path, "renamed.py")); err != nil {
		t.Fatal(err)
	}
	state, err := r.GitState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplyGit(ctx, GitRequest{Action: "stage", Revision: state.Revision, Paths: []string{"a.py", "renamed.py"}}); err != nil {
		t.Fatal(err)
	}
	state, err = r.GitState(ctx)
	if err != nil || len(state.Files) != 1 || state.Files[0].OldPath != "a.py" {
		t.Fatalf("rename not represented: %+v %v", state, err)
	}
	if _, err = r.ApplyGit(ctx, GitRequest{Action: "unstage", Revision: state.Revision, Paths: []string{"renamed.py"}}); err != nil {
		t.Fatal(err)
	}
	staged, _ := r.Git(ctx, "diff", "--cached", "--name-only")
	if staged != "" {
		t.Fatal("unstage failed to restore both rename paths")
	}
	if err = os.WriteFile(filepath.Join(r.Path, "binary.bin"), []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	diff, _, err := r.ClientDiff(ctx, "binary.bin", "working", "")
	if err != nil || !strings.Contains(diff, "Binary files") {
		t.Fatalf("binary diff: %q %v", diff, err)
	}
}
