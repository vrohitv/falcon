package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lines(n int, change map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if v, ok := change[i]; ok {
			b.WriteString(v + "\n")
		} else {
			b.WriteString("line " + string(rune('a'+i%26)) + " " + strings.Repeat("x", i%7) + "\n")
		}
	}
	return b.String()
}

// Staging and discarding single hunks, and refusing a hunk that changed after it was shown.
func TestHunkStageDiscardUnstageAndStaleness(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Path, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(r.Path, name))
		return string(b)
	}
	git := func(args ...string) {
		t.Helper()
		if _, err := r.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	write("long.txt", lines(40, nil))
	git("add", "long.txt")
	git("-c", "commit.gpgSign=false", "commit", "-qm", "long")
	// Two changes far apart (two hunks), and a new file.
	write("long.txt", lines(40, map[int]string{3: "FIRST change", 35: "SECOND change"}))
	write("new.txt", "brand new\n")
	// The files with changes in an area, each with its hunks as Source Control shows them.
	review := func(mode string) []ReviewFile {
		t.Helper()
		state, err := r.GitState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := []ReviewFile{}
		for _, f := range state.Files {
			if (mode == "staged" && f.Index == "") || (mode == "unstaged" && f.Worktree == "") {
				continue
			}
			raw, err := r.hunkDiff(ctx, f.Path, mode == "staged", f.Worktree == "?")
			if err != nil {
				t.Fatal(err)
			}
			files, _ := ParseDiff(raw)
			out = append(out, files...)
		}
		return out
	}
	file := func(rv []ReviewFile, path string) *ReviewFile {
		for i := range rv {
			if rv[i].Path == path {
				return &rv[i]
			}
		}
		return nil
	}
	rv := review("unstaged")
	long, fresh := file(rv, "long.txt"), file(rv, "new.txt")
	if long == nil || fresh == nil || len(long.Hunks) != 2 || fresh.Status != "added" || long.Additions != 2 || long.Deletions != 2 {
		t.Fatalf("review: %+v", rv)
	}
	apply := func(action, path, hunk string) (string, error) {
		return r.ApplyGit(ctx, GitRequest{Action: action, Paths: []string{path}, Hunk: hunk, Revision: "stale-is-fine-for-hunks"})
	}
	if _, err := apply("hunk.stage", "long.txt", long.Hunks[0].ID); err != nil {
		t.Fatal(err)
	}
	staged := review("staged")
	if f := file(staged, "long.txt"); f == nil || len(f.Hunks) != 1 || !strings.Contains(f.Hunks[0].Body, "FIRST") {
		t.Fatalf("staged: %+v", staged)
	}
	rv = review("unstaged")
	long = file(rv, "long.txt")
	if long == nil || len(long.Hunks) != 1 || !strings.Contains(long.Hunks[0].Body, "SECOND") {
		t.Fatalf("unstaged after accept: %+v", rv)
	}
	// The file changes after the hunk was shown: the old ID no longer applies.
	shown := long.Hunks[0].ID
	write("long.txt", lines(40, map[int]string{3: "FIRST change", 35: "SECOND change, revised"}))
	if _, err := apply("hunk.discard", "long.txt", shown); err == nil || !strings.Contains(err.Error(), "edited since") {
		t.Fatalf("stale hunk applied: %v", err)
	}
	long = file(review("unstaged"), "long.txt")
	if _, err := apply("hunk.discard", "long.txt", long.Hunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := read("long.txt"); !strings.Contains(got, "FIRST change") || strings.Contains(got, "SECOND") {
		t.Fatalf("after reject: %q", got)
	}
	if file(review("unstaged"), "long.txt") != nil {
		t.Fatal("long.txt still has unreviewed changes")
	}
	// Unstaging moves it back, keeping the working tree.
	staged = review("staged")
	if _, err := apply("hunk.unstage", "long.txt", file(staged, "long.txt").Hunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if file(review("staged"), "long.txt") != nil || file(review("unstaged"), "long.txt") == nil || !strings.Contains(read("long.txt"), "FIRST change") {
		t.Fatal("unaccept did not move the change back")
	}
	// A new file: staging adds it; discarding another new file deletes it.
	fresh = file(review("unstaged"), "new.txt")
	if _, err := apply("hunk.stage", "new.txt", fresh.Hunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if f := file(review("staged"), "new.txt"); f == nil || f.Status != "added" {
		t.Fatal("new file not accepted")
	}
	write("junk.txt", "scratch\n")
	junk := file(review("unstaged"), "junk.txt")
	if _, err := apply("hunk.discard", "junk.txt", junk.Hunks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "junk.txt")); !os.IsNotExist(err) {
		t.Fatal("rejected new file still exists")
	}
	// Showing a new file's diff never adds it to the index.
	write("shown-only.txt", "x\n")
	review("unstaged")
	if out, _ := r.Git(ctx, "ls-files", "--", "shown-only.txt"); out != "" {
		t.Fatal("showing a diff touched the index")
	}
}

func TestParseDiffNamesQuotedAndBinary(t *testing.T) {
	raw := "diff --git \"a/sp ace.txt\" \"b/sp ace.txt\"\nindex 1..2 100644\n--- \"a/sp ace.txt\"\n+++ \"b/sp ace.txt\"\n@@ -1 +1 @@\n-a\n+b\n" +
		"diff --git a/img.png b/img.png\nnew file mode 100644\nindex 0000000..1\nBinary files /dev/null and b/img.png differ\n" +
		"diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n"
	files, _ := ParseDiff(raw)
	if len(files) != 3 || files[0].Path != "sp ace.txt" || !files[1].Binary || files[1].Status != "added" || files[1].Path != "img.png" || files[2].Status != "renamed" || files[2].OldPath != "old.go" || files[2].Path != "new.go" {
		t.Fatalf("%+v", files)
	}
}

// Staging, unstaging and discarding chosen lines of a hunk, as git add -p's line editing does.
func TestLineStaging(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	path := filepath.Join(r.Path, "list.txt")
	os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644)
	if _, err := r.Git(ctx, "add", "list.txt"); err != nil {
		t.Fatal(err)
	}
	r.Git(ctx, "-c", "commit.gpgSign=false", "commit", "-qm", "list")
	// One hunk: two -/+ pairs and an addition.
	os.WriteFile(path, []byte("one\nTWO\nthree\nFOUR\nfive\n"), 0o644)
	hunk := func(staged bool) (ReviewHunk, []string) {
		t.Helper()
		raw, err := r.hunkDiff(ctx, "list.txt", staged, false)
		if err != nil {
			t.Fatal(err)
		}
		files, _ := ParseDiff(raw)
		if len(files) != 1 || len(files[0].Hunks) != 1 {
			t.Fatalf("diff: %q", raw)
		}
		h := files[0].Hunks[0]
		return h, strings.Split(strings.TrimSuffix(h.Body, "\n"), "\n")
	}
	indexOf := func(lines []string, want string) int {
		for i, l := range lines {
			if l == want {
				return i
			}
		}
		t.Fatalf("%q not in %q", want, lines)
		return -1
	}
	apply := func(action string, h ReviewHunk, lines ...int) {
		t.Helper()
		if _, err := r.ApplyGit(ctx, GitRequest{Action: action, Paths: []string{"list.txt"}, Hunk: h.ID, Lines: lines}); err != nil {
			t.Fatal(err)
		}
	}
	staged := func() string { out, _ := r.Git(ctx, "show", ":list.txt"); return out }
	h, lines := hunk(false)
	// Stage only "two" → "TWO".
	apply("lines.stage", h, indexOf(lines, "-two"), indexOf(lines, "+TWO"))
	if got := staged(); got != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("staged after lines.stage: %q", got)
	}
	// Stage the new last line alone.
	h, lines = hunk(false)
	apply("lines.stage", h, indexOf(lines, "+five"))
	if got := staged(); got != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("staged after adding five: %q", got)
	}
	// Unstage just "five".
	h, lines = hunk(true)
	apply("lines.unstage", h, indexOf(lines, "+five"))
	if got := staged(); got != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("staged after unstaging five: %q", got)
	}
	// Discard the unstaged "FOUR" (the working tree goes back to "four"), keep "five".
	h, lines = hunk(false)
	apply("lines.discard", h, indexOf(lines, "-four"), indexOf(lines, "+FOUR"))
	if b, _ := os.ReadFile(path); string(b) != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("worktree after discard: %q", b)
	}
	// Choosing only context lines is refused.
	h, lines = hunk(false)
	if _, err := r.ApplyGit(ctx, GitRequest{Action: "lines.stage", Paths: []string{"list.txt"}, Hunk: h.ID, Lines: []int{indexOf(lines, " three")}}); err == nil {
		t.Fatal("context-only selection accepted")
	}
	// Part of a new file.
	os.WriteFile(filepath.Join(r.Path, "new.txt"), []byte("a\nb\nc\n"), 0o644)
	raw, _ := r.hunkDiff(ctx, "new.txt", false, true)
	files, _ := ParseDiff(raw)
	nh := files[0].Hunks[0]
	nl := strings.Split(strings.TrimSuffix(nh.Body, "\n"), "\n")
	if _, err := r.ApplyGit(ctx, GitRequest{Action: "lines.stage", Paths: []string{"new.txt"}, Hunk: nh.ID, Lines: []int{indexOf(nl, "+a"), indexOf(nl, "+c")}}); err != nil {
		t.Fatal(err)
	}
	if out, _ := r.Git(ctx, "show", ":new.txt"); out != "a\nc\n" {
		t.Fatalf("new file partly staged: %q", out)
	}
}

func TestGraphShowsEveryBranchWithRefs(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if _, err := r.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("switch", "-qc", "side")
	os.WriteFile(filepath.Join(r.Path, "s.txt"), []byte("s\n"), 0o644)
	run("add", "s.txt")
	run("-c", "commit.gpgSign=false", "commit", "-qm", "side work")
	run("switch", "-q", "main")
	os.WriteFile(filepath.Join(r.Path, "m.txt"), []byte("m\n"), 0o644)
	run("add", "m.txt")
	run("-c", "commit.gpgSign=false", "commit", "-qm", "main work")
	run("tag", "v1")
	all, err := r.Graph(ctx, true, 0, 50)
	if err != nil || len(all.Commits) != 3 {
		t.Fatalf("%v %+v", err, all)
	}
	head, _ := r.Graph(ctx, false, 0, 50)
	if len(head.Commits) != 2 {
		t.Fatalf("HEAD only: %+v", head)
	}
	refs := ""
	for _, c := range all.Commits {
		refs += strings.Join(c.Refs, ",") + ";"
	}
	if !strings.Contains(refs, "HEAD -> main") || !strings.Contains(refs, "tag: v1") || !strings.Contains(refs, "side") {
		t.Fatalf("refs: %s", refs)
	}
	page, _ := r.Graph(ctx, true, 0, 2)
	if len(page.Commits) != 2 || page.Next != 2 {
		t.Fatalf("paging: %+v", page)
	}
}
