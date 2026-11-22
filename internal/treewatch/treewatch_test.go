package treewatch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func next(t *testing.T, w *Watcher, want func(Change) bool) Change {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c := <-w.C:
			if want(c) {
				return c
			}
		case <-deadline:
			t.Fatal("no matching change arrived")
		}
	}
}

func has(p string) func(Change) bool {
	return func(c Change) bool { return slices.Contains(c.Paths, p) }
}

// Writes, new files in new directories, removals and renames arrive; .git and ignored directories do not.
func TestChangesArriveAndIgnoredOnesDoNot(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".git", "src", "node_modules/pkg"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "src/a.go"), []byte("a"), 0o644)
	w, err := Start(root, func(dir string) bool { return dir == "node_modules" })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	os.WriteFile(filepath.Join(root, "src/a.go"), []byte("changed"), 0o644)
	next(t, w, has("src/a.go"))

	// A directory made and filled at once: the directory is reported, and later writes inside it too.
	os.MkdirAll(filepath.Join(root, "new/deep"), 0o755)
	os.WriteFile(filepath.Join(root, "new/deep/b.go"), []byte("b"), 0o644)
	c := next(t, w, func(c Change) bool {
		return slices.Contains(c.Paths, "new") || slices.Contains(c.Paths, "new/deep/b.go")
	})
	if !c.Structure {
		t.Fatal("a new directory is a change of structure")
	}
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "new/deep/b.go"), []byte("bb"), 0o644)
	next(t, w, has("new/deep/b.go"))

	os.Rename(filepath.Join(root, "new"), filepath.Join(root, "moved"))
	next(t, w, func(c Change) bool {
		return c.Structure && (slices.Contains(c.Paths, "new") || slices.Contains(c.Paths, "moved"))
	})

	os.Remove(filepath.Join(root, "src/a.go"))
	next(t, w, func(c Change) bool { return c.Structure && slices.Contains(c.Paths, "src/a.go") })

	// Nothing from .git or node_modules.
	os.WriteFile(filepath.Join(root, ".git/index"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "node_modules/pkg/i.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "marker"), []byte("x"), 0o644)
	c = next(t, w, has("marker"))
	for _, p := range c.Paths {
		if p == ".git/index" || p == "node_modules/pkg/i.js" {
			t.Fatalf("ignored change reported: %v", c.Paths)
		}
	}
}

// A burst of changes is one batch, not one per file.
func TestBurstIsBatched(t *testing.T) {
	root := t.TempDir()
	w, err := Start(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 200; i++ {
		os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), []byte("x"), 0o644)
	}
	got := 0
	batches := 0
	deadline := time.After(3 * time.Second)
	for got < 200 {
		select {
		case c := <-w.C:
			batches++
			got += len(c.Paths)
		case <-deadline:
			t.Fatalf("only %d of 200 paths arrived", got)
		}
	}
	if batches > 10 {
		t.Fatalf("200 writes came as %d batches", batches)
	}
}
