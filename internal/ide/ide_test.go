package ide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowedSourceEscapesHTMLAndInvalidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.ts")
	src := strings.Repeat("const item = '<script>alert(1)</script>';\n", 12000)
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	doc, err := Open(path, "large.ts")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := doc.Lines(9000, 9080)
	if len(rows) != 80 {
		t.Fatal("window length")
	}
	for _, row := range rows {
		if strings.Contains(row, "<script>") {
			t.Fatal("unescaped source HTML")
		}
	}
	oldHash := doc.Hash
	if err = os.WriteFile(path, []byte("short\n"), 0644); err != nil {
		t.Fatal(err)
	}
	doc, err = Open(path, "large.ts")
	if err != nil || doc.Total != 2 || doc.Hash == oldHash {
		t.Fatal("stale source returned after truncation")
	}
}
func TestFuzzyBasenameAndBoundaryRanking(t *testing.T) {
	files := Entries([]string{"very/long/read/me/path/index.ts", "README.md", "web/ReadModel.ts", "internal/reader.go"})
	hits := FuzzyFind(files, "readme", 10)
	if len(hits) == 0 || hits[0].Path != "README.md" {
		t.Fatalf("unexpected ranking %+v", hits)
	}
	if hits := FuzzyFind(files, "zzzzz", 10); len(hits) != 0 {
		t.Fatal("returned non-matching path")
	}
}
func TestDiffLineNumbers(t *testing.T) {
	hunks := HighlightDiff("a.py", "@@ -3,2 +3,2 @@ function\n-old\n+new\n same\n")
	if len(hunks) != 1 || len(hunks[0].Rows) != 3 {
		t.Fatal(hunks)
	}
	if hunks[0].Rows[0].OldLine != 3 || hunks[0].Rows[1].NewLine != 3 || hunks[0].Rows[2].NewLine != 4 {
		t.Fatal("diff line mapping")
	}
}

func TestLexerChoice(t *testing.T) {
	cases := map[string]string{"go.mod": "Go Module", "go.work": "Go Module", "main.go": "Go", "Cargo.lock": "TOML", "App.svelte": "Svelte"}
	for name, want := range cases {
		l := lexerFor(name, "")
		if l == nil || l.Config().Name != want {
			t.Errorf("%s: got %v, want %s", name, l, want)
		}
	}
	for _, name := range []string{"go.sum", "LICENSE", ".gitignore", "notes"} {
		if l := lexerFor(name, "some words here\nand more"); l != nil {
			t.Errorf("%s should be plain text, got %s", name, l.Config().Name)
		}
	}
	if l := lexerFor("run", "#!/bin/bash\necho hi\n"); l == nil {
		t.Error("a #! script should be recognised")
	}
	// go.mod versions are numbers, never errors.
	out := strings.Join(HighlightText("go.mod", "module example.com/x\n\ngo 1.26.0\n\nrequire golang.org/x/net v0.0.0-20241129210726-2c02b8208cf8 // indirect\n"), "\n")
	if strings.Contains(out, `class="err"`) || !strings.Contains(out, "v0.0.0-20241129210726-2c02b8208cf8") {
		t.Fatalf("go.mod highlighting: %s", out)
	}
}

func TestFuzzyPrefersNamesWordStartsAndTightMatches(t *testing.T) {
	files := Entries([]string{
		"internal/server/git.go", "web/src/GitClient.svelte", "internal/repository/git_client.go",
		"docs/legit-guide.md", "web/src/graph.ts", "internal/index/index.go", "web/src/App.svelte",
	})
	first := func(q string) string {
		hits := FuzzyFind(files, q, 10)
		if len(hits) == 0 {
			return ""
		}
		return hits[0].Path
	}
	cases := map[string]string{
		"git.go":    "internal/server/git.go",
		"gitclient": "web/src/GitClient.svelte",
		"git_cl":    "internal/repository/git_client.go",
		"gc":        "web/src/GitClient.svelte",
		"index":     "internal/index/index.go",
		"app":       "web/src/App.svelte",
		"srvgit":    "internal/server/git.go",
	}
	for q, want := range cases {
		if got := first(q); got != want {
			t.Errorf("%q: got %q, want %q (all: %+v)", q, got, want, FuzzyFind(files, q, 10))
		}
	}
	// Positions point at the matched bytes of the path.
	h := FuzzyFind(files, "app", 1)[0]
	for i, p := range h.Pos {
		if h.Path[p]|0x20 != "app"[i] {
			t.Fatalf("positions %v do not spell the query in %s", h.Pos, h.Path)
		}
	}
}

func TestHighlightWindowsPassAndDiffs(t *testing.T) {
	dir := t.TempDir()
	// A block comment that opens before the second window and closes inside it.
	var b strings.Builder
	b.WriteString("package x\n")
	for i := 0; i < 995; i++ {
		b.WriteString("var a = 1\n")
	}
	b.WriteString("/*\n")
	for i := 0; i < 20; i++ {
		b.WriteString("still a comment\n")
	}
	b.WriteString("*/\nfunc f() {}\n")
	path := filepath.Join(dir, "x.go")
	os.WriteFile(path, []byte(b.String()), 0o644)
	doc, err := Open(path, "x.go")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Lang != "Go" || doc.Total != strings.Count(b.String(), "\n")+1 {
		t.Fatalf("doc: %s %d", doc.Lang, doc.Total)
	}
	lines, _ := doc.Lines(1005, 1006)
	if !strings.Contains(lines[0], `class="c"`) {
		t.Fatalf("a comment opened before the window is still a comment in it: %q", lines[0])
	}
	if doc.Raw(1) != "package x" {
		t.Fatalf("raw: %q", doc.Raw(1))
	}
	// Small files become exact once the background pass is done.
	for i := 0; i < 200; i++ {
		if done, _ := doc.Exact(); done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if done, coming := doc.Exact(); !done || coming {
		t.Fatalf("exact: %v %v", done, coming)
	}
	if _, exact := doc.Lines(0, 10); !exact {
		t.Fatal("lines after the pass should be exact")
	}
	// Width counts tabs as four columns.
	os.WriteFile(filepath.Join(dir, "t.txt"), []byte("\tab\nx\n"), 0o644)
	if d, _ := Open(filepath.Join(dir, "t.txt"), "t.txt"); d.MaxCols != 6 {
		t.Fatalf("width %d lang %s", d.MaxCols, d.Lang)
	}
	// Both sides of a diff are highlighted.
	hunks := HighlightDiff("a.go", "@@ -1,2 +1,2 @@\n-func old() {}\n+func new() {}\n // same\n")
	if len(hunks) != 1 || !strings.Contains(hunks[0].Rows[0].HTML, `class="k"`) || !strings.Contains(hunks[0].Rows[1].HTML, `class="k"`) || !strings.Contains(hunks[0].Rows[2].HTML, `class="c"`) {
		t.Fatalf("diff html: %+v", hunks)
	}
	if hunks[0].Rows[0].At != 1 {
		t.Fatalf("a deletion sits before working-tree line 1: %+v", hunks[0].Rows[0])
	}
}

func TestShapeIsIndentAndLengthPerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.go")
	os.WriteFile(path, []byte("package x\n\nfunc f() {\n\treturn\n}\n"), 0o644)
	d, err := Open(path, "s.go")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 9, 0, 0, 0, 10, 4, 10, 0, 1, 0, 0}
	if got := d.Shape(); string(got) != string(want) {
		t.Fatalf("shape %v, want %v", got, want)
	}
}
