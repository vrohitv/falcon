package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// The listing (a walk with the rules in memory) is exactly what git lists, through the cases that make
// ignore rules hard: rules at several levels, negation, directory-only and anchored patterns, a
// tracked file a rule matches, an ignored directory holding a tracked file beside untracked ones,
// info/exclude, and a repository inside the folder.
func TestListingMatchesGit(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	runGit(t, dir, "init", "-q")
	write(".gitignore", "*.log\nbuild/\n/root-only.txt\n!keep.log\nvendor/\ndocs/**/*.tmp\n")
	write("src/.gitignore", "generated/\n*.gen.go\n!special.gen.go\n")
	write(".git/info/exclude", "secret.txt\n")
	for _, p := range []string{
		"main.go", "a.log", "keep.log", "root-only.txt", "sub/root-only.txt", "build/out.bin", "sub/build/x",
		"src/a.go", "src/b.gen.go", "src/special.gen.go", "src/generated/c.go", "src/deep/d.gen.go",
		"docs/x/y.tmp", "docs/x/y.md", "secret.txt", "sub/secret.txt", "vendor/tracked.go", "vendor/untracked.go",
		"weird name with spaces.txt",
	} {
		write(p, "x\n")
	}
	runGit(t, dir, "add", "-f", "vendor/tracked.go", "build/out.bin")
	runGit(t, dir, "add", "main.go")
	// A repository inside the folder.
	os.MkdirAll(filepath.Join(dir, "nested"), 0o755)
	runGit(t, filepath.Join(dir, "nested"), "init", "-q")
	write("nested/inner.go", "x\n")

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.Files(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{}
	for _, p := range strings.Split(runGit(t, dir, "-c", "core.quotePath=false", "ls-files", "-z", "--cached", "--others", "--exclude-standard"), "\x00") {
		if p != "" && !strings.HasSuffix(p, "/") {
			want = append(want, p)
		}
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("listing differs from git\n got: %v\nwant: %v", got, want)
	}

	// Each path's ignored state, as git check-ignore reports it.
	ig := r.Ignorer()
	for _, c := range []struct {
		path string
		dir  bool
	}{
		{"a.log", false}, {"keep.log", false}, {"build", true}, {"sub/build", true}, {"root-only.txt", false},
		{"sub/root-only.txt", false}, {"src/generated", true}, {"src/b.gen.go", false}, {"src/special.gen.go", false},
		{"docs/x/y.tmp", false}, {"secret.txt", false}, {"vendor/untracked.go", false}, {"vendor/tracked.go", false},
		{"build/out.bin", false}, {"main.go", false},
	} {
		cmd := exec.Command("git", "check-ignore", "-q", "--", c.path)
		cmd.Dir = dir
		wantIgnored := cmd.Run() == nil
		if got := ig.Ignored(c.path, c.dir); got != wantIgnored {
			t.Errorf("%s: ignored=%v, git says %v", c.path, got, wantIgnored)
		}
	}

	// Rules and the index are read again when they change.
	write(".gitignore", "*.log\n")
	ig.Reload()
	if ig.Ignored("root-only.txt", false) {
		t.Fatal("a removed rule still applies after Reload")
	}
	runGit(t, dir, "add", "-f", "a.log")
	ig.Sync()
	if ig.Ignored("a.log", false) {
		t.Fatal("a newly tracked file is still ignored after Sync")
	}
}
