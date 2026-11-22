package textsearch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func run(t *testing.T, files map[string]string, o Options) []string {
	t.Helper()
	dir := t.TempDir()
	names := []string{}
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
		names = append(names, p)
	}
	sort.Strings(names)
	m, err := Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	hits, _, err := Search(context.Background(), dir, names, m)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, h := range hits {
		out = append(out, fmt.Sprintf("%s:%d:%d:%s", h.Path, h.Line, h.Column, h.Content))
	}
	return out
}

func TestSearchLikeGrep(t *testing.T) {
	files := map[string]string{
		"a.go":   "package a\nfunc Greet() {}\n// greet everyone\nvar greeting = Greet()\r\n",
		"b.txt":  "no match here\nGREET loudly",
		"bin.so": "Greet\x00\x01binary",
	}
	cases := []struct {
		o    Options
		want string
	}{
		// Case-insensitive by default; each line once, with its first match's column; CR dropped.
		{Options{Query: "greet"}, "a.go:2:6:func Greet() {}|a.go:3:4:// greet everyone|a.go:4:5:var greeting = Greet()|b.txt:2:1:GREET loudly"},
		{Options{Query: "Greet", Case: true}, "a.go:2:6:func Greet() {}|a.go:4:16:var greeting = Greet()"},
		// Whole words: a later occurrence counts when an earlier one is inside a word.
		{Options{Query: "greet", Word: true}, "a.go:2:6:func Greet() {}|a.go:3:4:// greet everyone|a.go:4:16:var greeting = Greet()|b.txt:2:1:GREET loudly"},
		{Options{Query: `^func \w+`, Regex: true}, "a.go:2:1:func Greet() {}"},
		{Options{Query: `G.eet\(\)$`, Regex: true, Case: true}, "a.go:4:16:var greeting = Greet()"}, // CR LF line
		{Options{Query: "nothing"}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(run(t, files, c.o), "|"); got != c.want {
			t.Errorf("%+v\n got %s\nwant %s", c.o, got, c.want)
		}
	}
	// Non-ASCII text, ignoring case, goes through the regular expression path.
	if got := run(t, map[string]string{"u.txt": "Ünïcode here\n"}, Options{Query: "üNÏ"}); len(got) != 1 {
		t.Errorf("non-ASCII ignoring case: %v", got)
	}
	if _, err := Compile(Options{Query: "(", Regex: true}); err == nil {
		t.Error("a bad regular expression is reported")
	}
	// The literal pieces a regular expression's matches must contain.
	for expr, want := range map[string]string{
		`func New\w+Informer`: "func New|Informer", `(handleRequest)\(`: "handleRequest", `a|bcd`: "", `\d+`: "", `^package main$`: "package main",
	} {
		if got := strings.Join(RequiredLiterals(expr, true), "|"); got != want {
			t.Errorf("RequiredLiterals(%q) = %q, want %q", expr, got, want)
		}
	}
	// At most PerFile lines from one file.
	if got := run(t, map[string]string{"many.txt": strings.Repeat("x\n", 100)}, Options{Query: "x"}); len(got) != 50 {
		t.Errorf("per-file limit: %d lines", len(got))
	}
}
