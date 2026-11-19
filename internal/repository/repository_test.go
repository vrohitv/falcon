package repository

import (
	"context"
	"github.com/vrohitv/falcon/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("print('before')\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = r.Git(ctx, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Git(ctx, "-c", "commit.gpgSign=false", "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestConfinedReadsAndOptimisticWrites(t *testing.T) {
	r := fixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(r.Path, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/etc/passwd", ".git/config", ".env", "escape/secret", "a.py/../../outside"} {
		if _, err := r.Read(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	file, err := r.Read("a.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Write("a.py", "oops", "stale"); err != domain.ErrConflict {
		t.Fatalf("expected conflict: %v", err)
	}
	if _, err = r.Write("a.py", "print('after')\n", file.Hash); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryFiltersNeverExecute(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	if _, err := r.Git(ctx, "config", "filter.fixture.clean", "touch CONTROL_PLANE_EXECUTED; cat"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "config", "filter.fixture.required", "true"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Path, ".gitattributes"), []byte("*.py filter=fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Reading status, diffs and file contents never runs the repository's filters.
	os.WriteFile(filepath.Join(r.Path, "a.py"), []byte("changed\n"), 0644)
	if _, err := r.GitState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ClientDiff(ctx, "a.py", "working", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "CONTROL_PLANE_EXECUTED")); !os.IsNotExist(err) {
		t.Fatalf("filter executed: %v", err)
	}
}

func TestUnbornRepositoryAndSubdirectoryBinding(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", output, err)
	}
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if status := repo.Status(context.Background()); status.Head != "" || status.Branch != "main" {
		t.Fatalf("invalid unborn status: %+v", status)
	}
	sub := filepath.Join(root, "nested")
	if err = os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(sub); err == nil {
		t.Fatal("nested path accepted as repository root")
	}
}

// A filter configured after git was already asked about filters is still turned off: the answer is
// cached only until the configuration changes.
func TestFilterAddedLaterNeverExecutes(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	if _, err := r.GitState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git(ctx, "config", "filter.late.clean", "touch LATE_FILTER_RAN; cat"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.Path, ".gitattributes"), []byte("*.py filter=late\n"), 0644)
	os.WriteFile(filepath.Join(r.Path, "a.py"), []byte("changed\n"), 0644)
	if _, err := r.GitState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ClientDiff(ctx, "a.py", "working", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "LATE_FILTER_RAN")); !os.IsNotExist(err) {
		t.Fatal("a filter configured later ran")
	}
}

// A link to something inside the folder opens; one leading outside (or into .git) does not.
func TestSymlinksInsideAreFollowedOutsideRefused(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret\n"), 0o644)
	os.MkdirAll(filepath.Join(r.Path, "docs"), 0o755)
	os.WriteFile(filepath.Join(r.Path, "docs", "AGENTS.md"), []byte("# agents\n"), 0o644)
	os.Symlink("docs/AGENTS.md", filepath.Join(r.Path, "CLAUDE.md"))
	os.Symlink("docs", filepath.Join(r.Path, "manual"))
	os.Symlink(outside, filepath.Join(r.Path, "leak.txt"))
	os.Symlink(".git/config", filepath.Join(r.Path, "cfg"))
	if f, err := r.Read("CLAUDE.md"); err != nil || f.Content != "# agents\n" {
		t.Fatalf("inside link: %v %q", err, f.Content)
	}
	if f, err := r.Read("manual/AGENTS.md"); err != nil || f.Content != "# agents\n" {
		t.Fatalf("linked folder: %v", err)
	}
	for _, p := range []string{"leak.txt", "cfg"} {
		if _, err := r.Read(p); err == nil {
			t.Fatalf("%s readable", p)
		}
	}
	files, _ := r.Files(ctx)
	names := map[string]bool{}
	for _, f := range files {
		names[f.Path] = true
	}
	if !names["CLAUDE.md"] || names["leak.txt"] || names["cfg"] {
		t.Fatalf("listing: %v", names)
	}
	entries, _ := r.Directory(ctx, "")
	for _, e := range entries {
		switch e.Path {
		case "manual":
			if !e.Directory || e.Protected {
				t.Fatalf("linked folder entry: %+v", e)
			}
		case "leak.txt", "cfg":
			if !e.Protected {
				t.Fatalf("outside link open: %+v", e)
			}
		case "CLAUDE.md":
			if e.Protected {
				t.Fatalf("inside link closed: %+v", e)
			}
		}
	}
}

// With core.autocrlf in the owner's configuration (as Git for Windows sets it), a checkout with CRLF line
// endings is clean to Falcon too, though Falcon ignores the rest of that configuration.
func TestOwnersLineEndingSettingIsHonoured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tautocrlf = true\n[user]\n\tname = T\n\temail = t@example.test\n"), 0o644)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", b)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "crlf.txt"), []byte("one\r\ntwo\r\n"), 0o644)
	run("add", "crlf.txt")
	run("-c", "commit.gpgSign=false", "commit", "-qm", "crlf")
	// Make git look at the content again, not trust the cached file times.
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(dir, "crlf.txt"), later, later)
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.GitState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Clean {
		t.Fatalf("a CRLF checkout looks modified: %+v", st.Files)
	}
}
