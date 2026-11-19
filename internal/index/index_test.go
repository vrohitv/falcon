package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vrohitv/falcon/internal/repository"
)

func repo(t *testing.T, files map[string]string) *repository.Repository {
	t.Helper()
	dir := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s", b)
	}
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	r, err := repository.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSymbolsAreIndexedRefreshedAndSaved(t *testing.T) {
	r := repo(t, map[string]string{
		"a.go":       "package a\n\ntype Server struct{}\n\nfunc (s *Server) ServeHTTP() {}\n\nfunc NewServer() *Server { return nil }\n",
		"web/app.ts": "export function renderPage() {}\nexport class PageView {}\n",
		"README.md":  "# not code\n",
	})
	save := filepath.Join(t.TempDir(), "idx.gob")
	x := New(r, save)
	ctx := context.Background()
	x.Refresh(ctx)
	if !x.Ready() {
		t.Fatal("not ready")
	}
	if got := x.Lookup("ServeHTTP"); len(got) != 1 || got[0].Name != "Server.ServeHTTP" || got[0].Line != 5 {
		t.Fatalf("method by its own name: %+v", got)
	}
	if got := x.Lookup("renderPage"); len(got) != 1 || got[0].Path != "web/app.ts" {
		t.Fatalf("%+v", got)
	}
	// Ranking: exact, then prefix, then word start, then anywhere.
	res := x.Search("server", 10)
	if len(res) < 3 || res[0].Name != "Server" || res[1].Name != "Server.ServeHTTP" && res[1].Name != "NewServer" {
		t.Fatalf("search: %+v", res)
	}
	if res := x.Search("View", 10); len(res) != 1 || res[0].Name != "PageView" {
		t.Fatalf("case-sensitive word start: %+v", res)
	}
	// A change is picked up; a deleted file drops out; nothing else is reparsed.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(r.Path, "web/app.ts"), []byte("export function renderLayout() {}\n"), 0o644)
	os.Remove(filepath.Join(r.Path, "a.go"))
	x.Invalidate()
	x.Refresh(ctx)
	if len(x.Lookup("renderPage")) != 0 || len(x.Lookup("renderLayout")) != 1 || len(x.Lookup("Server")) != 0 {
		t.Fatalf("after change: %+v %+v", x.Lookup("renderLayout"), x.Lookup("Server"))
	}
	// A new index starts from the saved one, before any refresh.
	y := New(r, save)
	if !y.Ready() || len(y.Lookup("renderLayout")) != 1 {
		t.Fatal("saved index not loaded")
	}
}

func TestFilesAreServedFromMemoryAndRefreshed(t *testing.T) {
	r := repo(t, map[string]string{"one.txt": "1\n"})
	x := New(r, "")
	ctx := context.Background()
	files, err := x.Files(ctx)
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
	os.WriteFile(filepath.Join(r.Path, "two.txt"), []byte("2\n"), 0o644)
	// Still the cached listing at once…
	if files, _ := x.Files(ctx); len(files) != 1 {
		t.Fatal("not served from memory")
	}
	// …and the fresh one once marked out of date.
	x.Invalidate()
	if files, _ := x.Fresh(ctx); len(files) != 2 {
		t.Fatalf("not refreshed: %v", files)
	}
	res, err := x.Fuzzy(ctx, "two", 5)
	if err != nil || len(res) == 0 || res[0].Path != "two.txt" {
		t.Fatalf("fuzzy: %+v %v", res, err)
	}
}
