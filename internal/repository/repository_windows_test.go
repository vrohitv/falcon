//go:build windows

package repository

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vrohitv/falcon/internal/domain"
)

// Windows opens .git under other spellings (any case, trailing dots, the short name); none may reach it.
func TestGitFolderUnderEveryWindowsSpellingIsRefused(t *testing.T) {
	r := fixture(t)
	cfg := filepath.Join(r.Path, ".git", "config")
	before, _ := os.ReadFile(cfg)
	for _, p := range []string{".GIT/config", ".Git/config", ".git./config", ".git ./config", "GIT~1/config", "docs/../.GIT/config", "sub/.GIT/x"} {
		if f, err := r.Read(p); err == nil {
			t.Errorf("read %s: %q", p, f.Content)
		}
		if _, err := r.Write(p, "[core]\n\tfsmonitor = calc.exe\n", domain.Hash(before)); err == nil {
			t.Errorf("wrote %s", p)
		}
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Fatalf(".git/config was changed:\n%s", after)
	}
}

// A junction needs no privilege on Windows, and Go reports it as irregular, not as a link: it must be
// held to the same rule as a link (followed only when it leads inside the folder).
func TestJunctionsAreHeldToTheLinkRule(t *testing.T) {
	r := fixture(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644)
	os.MkdirAll(filepath.Join(r.Path, "docs"), 0o755)
	os.WriteFile(filepath.Join(r.Path, "docs", "guide.md"), []byte("# guide\n"), 0o644)
	junction := func(link, target string) {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %s", out)
		}
	}
	junction(filepath.Join(r.Path, "escape"), outside)
	junction(filepath.Join(r.Path, "manual"), filepath.Join(r.Path, "docs"))
	junction(filepath.Join(r.Path, "gitdir"), filepath.Join(r.Path, ".git"))
	if f, err := r.Read("escape/secret.txt"); err == nil {
		t.Fatalf("read outside through a junction: %q", f.Content)
	}
	if _, err := r.Write("escape/planted.txt", "x", ""); err == nil {
		t.Fatal("wrote outside through a junction")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a file landed outside the folder")
	}
	if _, err := r.Read("gitdir/config"); err == nil {
		t.Fatal("read .git through a junction")
	}
	if f, err := r.Read("manual/guide.md"); err != nil || f.Content != "# guide\n" {
		t.Fatalf("a junction inside the folder must open: %v %q", err, f.Content)
	}
	entries, err := r.Directory(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Path {
		case "escape", "gitdir":
			if !e.Protected {
				t.Errorf("%s leads outside but is shown open: %+v", e.Path, e)
			}
		case "manual":
			if !e.Directory || e.Protected {
				t.Errorf("manual leads inside and should open as a folder: %+v", e)
			}
		}
	}
}
