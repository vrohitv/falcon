package index

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The listing the watcher keeps (without walking the folder) stays equal to a fresh walk through new
// directories, renames, deletions, files in ignored directories and a changed .gitignore; and the
// language servers are told which files came and went.
func TestWatchedListingEqualsAWalk(t *testing.T) {
	r := repo(t, map[string]string{".gitignore": "out/\n*.tmp\n", "a/x.go": "package a\n", "b/y.go": "package b\n"})
	x := New(r, "")
	ctx := context.Background()
	if _, err := x.Files(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() error {
		if !x.Live() {
			return fmt.Errorf("not watching")
		}
		return nil
	})
	var mu sync.Mutex
	created, deleted := map[string]bool{}, map[string]bool{}
	stop := x.OnFiles(func(es []FileEvent) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range es {
			switch e.Kind {
			case Created:
				created[e.Path] = true
				delete(deleted, e.Path)
			case Deleted:
				deleted[e.Path] = true
				delete(created, e.Path)
			}
		}
	})
	defer stop()
	rng := rand.New(rand.NewSource(3))
	write := func(p string) {
		os.MkdirAll(filepath.Dir(filepath.Join(r.Path, p)), 0o755)
		os.WriteFile(filepath.Join(r.Path, p), []byte("x"), 0o644)
	}
	same := func() error {
		want, err := r.Files(ctx)
		if err != nil {
			return err
		}
		got, _ := x.Files(ctx)
		var a, b []string
		for _, f := range want {
			a = append(a, f.Path)
		}
		for _, f := range got {
			b = append(b, f.Path)
		}
		if strings.Join(a, ",") != strings.Join(b, ",") {
			return fmt.Errorf("watched listing %v\nwalk %v", b, a)
		}
		return nil
	}
	for round := 0; round < 6; round++ {
		d := fmt.Sprintf("new%d", round)
		write(d + "/deep/f.go")
		write(d + "/g.txt")
		write(fmt.Sprintf("out/ignored%d.go", round)) // ignored directory
		write(fmt.Sprintf("a/skip%d.tmp", round))     // ignored file
		if round > 0 {
			prev := fmt.Sprintf("new%d", round-1)
			if rng.Intn(2) == 0 {
				os.Rename(filepath.Join(r.Path, prev), filepath.Join(r.Path, prev+"moved"))
			} else {
				os.RemoveAll(filepath.Join(r.Path, prev))
			}
		}
		eventually(t, same)
	}
	mu.Lock()
	if !created["new5/deep/f.go"] || !deleted["new0/deep/f.go"] {
		t.Fatalf("file events: created %v deleted %v", created, deleted)
	}
	mu.Unlock()
	// A rule removed: the files it hid appear.
	os.WriteFile(filepath.Join(r.Path, ".gitignore"), []byte("*.tmp\n"), 0o644)
	eventually(t, func() error {
		got, _ := x.Files(ctx)
		for _, f := range got {
			if strings.HasPrefix(f.Path, "out/") {
				return same()
			}
		}
		return fmt.Errorf("files under out/ not listed after its rule was removed")
	})
}
