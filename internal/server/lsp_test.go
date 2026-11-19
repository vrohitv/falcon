package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/vrohitv/falcon/internal/lsp"
)

// A language server's answer becomes places the editor can open: precise columns (UTF-16 in, characters
// out), the line's text, and files outside the folder as read-only ~ext/ paths reachable only by the
// token Falcon issued.
func TestLocationsArePreciseAndReachOutsideTheFolder(t *testing.T) {
	h := newHarness(t, "")
	outside := filepath.Join(t.TempDir(), "pkg", "mod", "example.com", "lib@v1.0.0", "lib.go")
	os.MkdirAll(filepath.Dir(outside), 0o755)
	os.WriteFile(outside, []byte("package lib\n\n// é then the name\nfunc Thing() {}\n"), 0o644)
	inside := filepath.Join(h.a.Repo.Path, "main.go")
	reply, _ := json.Marshal([]map[string]any{
		{"uri": lsp.URI(inside), "range": map[string]any{"start": map[string]int{"line": 2, "character": 5}, "end": map[string]int{"line": 2, "character": 10}}},
		{"targetUri": lsp.URI(outside), "targetSelectionRange": map[string]any{"start": map[string]int{"line": 3, "character": 5}, "end": map[string]int{"line": 3, "character": 10}}},
		{"uri": "untitled:x", "range": map[string]any{"start": map[string]int{"line": 0, "character": 0}}},
	})
	locs := h.s.locations(h.a, reply)
	if len(locs) != 2 {
		t.Fatalf("locations: %+v", locs)
	}
	if l := locs[0]; l.Path != "main.go" || l.Line != 3 || l.Column != 5 || l.End != 10 || l.Text != "func Greet() string { return \"hi\" }" || l.External != "" {
		t.Fatalf("inside: %+v", l)
	}
	ext := locs[1]
	if ext.External != outside || ext.Line != 4 || ext.Column != 5 || ext.End != 10 || ext.Text != "func Thing() {}" {
		t.Fatalf("outside: %+v", ext)
	}
	if want := "example.com/lib@v1.0.0/lib.go"; filepath.Base(filepath.Dir(ext.Path)) == "" || ext.Path[len(ext.Path)-len(want):] != want {
		t.Fatalf("readable name: %s", ext.Path)
	}
	// The outside file opens read-only by its path, in this folder only.
	var win struct {
		Total    int    `json:"total"`
		Language string `json:"language"`
	}
	if code := h.json("GET", "/api/r/"+h.a.ID+"/code?path="+url.QueryEscape(ext.Path), "", &win); code != 200 || win.Total != 5 || win.Language != "Go" {
		t.Fatalf("open outside file: %d %+v", code, win)
	}
	if code := h.json("GET", "/api/r/"+h.b.ID+"/code?path="+url.QueryEscape(ext.Path), "", &win); code == 200 {
		t.Fatal("an outside file reached in one folder must not open in another")
	}
	// A made-up token reads nothing, and neither does any path that is not issued.
	for _, p := range []string{"~ext/0123456789abcdef01/etc/passwd", "~ext/../../etc/passwd", "../outside.go"} {
		if code := h.json("GET", "/api/r/"+h.a.ID+"/code?path="+url.QueryEscape(p), "", &win); code == 200 {
			t.Fatalf("%s was readable", p)
		}
	}
	// Columns: a UTF-16 column on a line with non-ASCII text becomes characters, and back.
	if got := characters("é😀x", 3); got != 2 {
		t.Fatalf("characters: %d", got)
	}
	if got := utf16Column("a\né😀x\n", 1, 2); got != 3 {
		t.Fatalf("utf16Column: %d", got)
	}
}

func TestLanguageServerStateIsPerFolder(t *testing.T) {
	h := newHarness(t, "")
	h.s.lspRuns.note(h.a.Repo.Path, "gopls", "failed", "boom")
	if r := h.s.lspRuns.latest(h.a.Repo.Path, "gopls"); r.State != "failed" {
		t.Fatalf("alpha: %+v", r)
	}
	if r := h.s.lspRuns.latest(h.b.Repo.Path, "gopls"); r.State != "" {
		t.Fatalf("another folder must not see alpha's server state: %+v", r)
	}
	// A project inside the folder counts as the folder's.
	h.s.lspRuns.note(filepath.Join(h.a.Repo.Path, "tools"), "gopls", "ready", "")
	if r := h.s.lspRuns.latest(h.a.Repo.Path, "gopls"); r.State != "ready" {
		t.Fatalf("nested project: %+v", r)
	}
	var st struct {
		Servers []struct {
			Server   string `json:"server"`
			Installs []struct{ Command, Where string }
		} `json:"servers"`
		Searched []string `json:"searched"`
	}
	h.json("GET", "/api/r/"+h.a.ID+"/lsp/status", "", &st)
	if len(st.Servers) == 0 || len(st.Searched) == 0 {
		t.Fatalf("status: %+v", st)
	}
	for _, s := range st.Servers {
		if len(s.Installs) == 0 || s.Installs[0].Command == "" || s.Installs[0].Where == "" {
			t.Fatalf("%s has no install command for this system", s.Server)
		}
	}
}
