package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s", b)
	}
	return dir
}

func TestFoldersAreAddedOnceSavedAndRestored(t *testing.T) {
	file := filepath.Join(t.TempDir(), "workspace.json")
	w, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	a, b := gitRepo(t, "alpha"), gitRepo(t, "beta")
	ra, err := w.Add(a, false)
	if err != nil {
		t.Fatal(err)
	}
	// A subfolder means its repository; adding again returns the same folder.
	again, err := w.Add(filepath.Join(a, "sub"), false)
	if err != nil || again.ID != ra.ID {
		t.Fatalf("subfolder: %v %v", again, err)
	}
	if _, err = w.Add(b, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ra.ID, "alpha-") || len(w.List()) != 2 {
		t.Fatalf("%s %d", ra.ID, len(w.List()))
	}
	restored, err := Open(file)
	if err != nil || len(restored.List()) != 2 || restored.List()[0].ID != ra.ID {
		t.Fatalf("restore: %v %+v", err, restored.List())
	}
	if err = restored.Remove(ra.ID); err != nil {
		t.Fatal(err)
	}
	again2, _ := Open(file)
	if len(again2.List()) != 1 {
		t.Fatal("removal not saved")
	}
}

func TestNotGitAndInit(t *testing.T) {
	w, _ := Open("")
	plain := t.TempDir()
	if _, err := w.Add(plain, false); err == nil || !strings.Contains(err.Error(), "not a Git repository") {
		t.Fatalf("%v", err)
	}
	r, err := w.Add(plain, true)
	if err != nil || r.Repo == nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := w.Add(filepath.Join(plain, "nope"), false); err == nil {
		t.Fatal("missing folder accepted")
	}
}

func TestSameNameDifferentPathsGetDifferentIDs(t *testing.T) {
	if ID("/a/app") == ID("/b/app") || !strings.HasPrefix(ID("/x/My App!"), "my-app-") {
		t.Fatal(ID("/a/app"), ID("/x/My App!"))
	}
}

func TestBrowseMarksRepositories(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "plain"), 0o755)
	os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)
	exec.Command("git", "init", "-q", filepath.Join(root, "repo")).Run()
	_, folders, err := Browse(root)
	if err != nil || len(folders) != 2 || folders[0].Name != "plain" || folders[0].Git || !folders[1].Git {
		t.Fatalf("%v %+v", err, folders)
	}
}
