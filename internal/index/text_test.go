package index

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func waitText(t *testing.T, x *Index) {
	t.Helper()
	x.BuildText()
	deadline := time.Now().Add(10 * time.Second)
	for !x.TextReady() {
		if time.Now().After(deadline) {
			t.Fatal("text index not built")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// matches is the truth: every file containing q (case-insensitively when q is lowercase).
func matches(t *testing.T, root string, files []string, q string) []string {
	out := []string{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		hay, needle := string(b), q
		if strings.ToLower(q) == q {
			hay = strings.ToLower(hay)
		}
		if strings.Contains(hay, needle) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

func superset(t *testing.T, got, want []string, q string) {
	t.Helper()
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("query %q: %s matches but was not a candidate (candidates %v)", q, w, got)
		}
	}
}

// The candidates always include every file that matches, through random edits, new files, deletions and
// immediate queries; and they are far fewer than all files.
func TestTextCandidatesNeverMissAMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	words := []string{"alpha", "Beta", "gamma", "handleRequest", "parseToken", "cache", "Server", "render", "index", "Ünïcode"}
	line := func() string {
		var b strings.Builder
		for i := 0; i < 6; i++ {
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteString(fmt.Sprint(rng.Intn(50)))
			b.WriteByte(' ')
		}
		return b.String() + "\n"
	}
	files := map[string]string{}
	for i := 0; i < 120; i++ {
		var b strings.Builder
		for j := 0; j < 5; j++ {
			b.WriteString(line())
		}
		files[fmt.Sprintf("d%d/f%d.txt", i%7, i)] = b.String()
	}
	files["big.bin"] = strings.Repeat("handleRequest7 ", 100000) // over the indexing limit: always searched
	r := repo(t, files)
	x := New(r, "")
	waitText(t, x)
	ctx := context.Background()
	list := func() []string {
		fs, _ := x.Fresh(ctx)
		out := []string{}
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	queries := []string{"handlerequest7", "HandleRequest", "parsetoken1", "cache4 ", "erve", "gamma33", "zzzz", "beta1", "ünïcode"}
	for round := 0; round < 6; round++ {
		for _, q := range queries {
			got, ok := x.Candidates(ctx, q)
			if !ok {
				continue
			}
			want := matches(t, r.Path, list(), q)
			superset(t, got, want, q)
			if q == "zzzz" && len(got) > 2 {
				t.Fatalf("a query found nowhere has %d candidates", len(got))
			}
		}
		// Change things, then query at once.
		for k := 0; k < 10; k++ {
			p := fmt.Sprintf("d%d/f%d.txt", rng.Intn(7), rng.Intn(120))
			os.WriteFile(filepath.Join(r.Path, p), []byte(line()+"handleRequest99 fresh"+fmt.Sprint(round)+"\n"), 0o644)
		}
		os.WriteFile(filepath.Join(r.Path, fmt.Sprintf("new%d.txt", round)), []byte("brandnewword"+fmt.Sprint(round)+"\n"), 0o644)
		os.Remove(filepath.Join(r.Path, fmt.Sprintf("d1/f%d.txt", 1+7*round)))
		for _, q := range []string{"handlerequest99", fmt.Sprintf("fresh%d", round), fmt.Sprintf("brandnewword%d", round)} {
			got, ok := x.Candidates(ctx, q)
			if !ok {
				t.Fatalf("index unavailable for %q", q)
			}
			superset(t, got, matches(t, r.Path, list(), q), q)
		}
	}
	// Narrow: a specific token is in few files.
	got, _ := x.Candidates(ctx, "brandnewword3")
	if len(got) > 3 {
		t.Fatalf("too many candidates for a rare word: %v", got)
	}
	if st := x.TextStatus(); !st.Ready || st.Files < 100 {
		t.Fatalf("%+v", st)
	}
}

// The text index is saved and, after a restart, answers from the saved copy, reading only what changed.
func TestTextIndexIsSavedAndReloaded(t *testing.T) {
	r := repo(t, map[string]string{"a.txt": "needle one\n", "b.txt": "nothing here\n"})
	save := filepath.Join(t.TempDir(), "idx.gob")
	x := New(r, save)
	waitText(t, x)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(strings.TrimSuffix(save, ".gob") + ".text.gob"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("text index not saved")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// While stopped, b.txt changes.
	os.WriteFile(filepath.Join(r.Path, "b.txt"), []byte("needle two\n"), 0o644)
	y := New(r, save)
	waitText(t, y)
	got, ok := y.Candidates(context.Background(), "needle")
	sort.Strings(got)
	if !ok || strings.Join(got, ",") != "a.txt,b.txt" {
		t.Fatalf("after reload: %v %v", got, ok)
	}
	if b := encode(nil, []int32{0, 3, 70, 70000}); fmt.Sprint(decode(nil, b)) != "[0 3 70 70000]" {
		t.Fatal("encode/decode")
	}
	if got := intersectEncoded([]int32{1, 3, 5, 70, 9000}, encode(nil, []int32{3, 4, 70, 71, 9000})); fmt.Sprint(got) != "[3 70 9000]" {
		t.Fatalf("intersectEncoded: %v", got)
	}
}

// A rebuild starts from the previous index: after edits, new files and deletions, the rebuilt index
// answers exactly as one built from scratch would (never missing a match, and no stale candidate for a
// file that lost the text or is gone).
func TestIncrementalRebuildEqualsAFreshBuild(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	words := []string{"alpha", "beta", "gamma", "delta", "handle", "token", "cache", "server", "render", "index"}
	text := func() string {
		var b strings.Builder
		for i := 0; i < 30; i++ {
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteString(fmt.Sprint(rng.Intn(30)))
			b.WriteByte(' ')
		}
		return b.String() + "\n"
	}
	files := map[string]string{}
	for i := 0; i < 200; i++ {
		files[fmt.Sprintf("p%d/f%03d.txt", i%9, i)] = text()
	}
	r := repo(t, files)
	x := New(r, "")
	waitText(t, x)
	ctx := context.Background()
	answer := func(ix *Index, q string) []string {
		got, ok := ix.Candidates(ctx, q)
		if !ok {
			t.Fatalf("index not usable for %q", q)
		}
		return got
	}
	queries := []string{"alpha1", "beta22", "handle7", "token", "cache29", "server3 ", "zzz", "newword"}
	for round := 0; round < 8; round++ {
		// Change a fifth of the files, add some, delete some.
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			switch rng.Intn(10) {
			case 0, 1:
				files[p] = text()
				if rng.Intn(3) == 0 {
					files[p] += "newword" + fmt.Sprint(round)
				}
				os.WriteFile(filepath.Join(r.Path, p), []byte(files[p]), 0o644)
			case 2:
				delete(files, p)
				os.Remove(filepath.Join(r.Path, p))
			}
		}
		for i := 0; i < 10; i++ {
			p := fmt.Sprintf("n%d/new%d_%d.txt", i%3, round, i)
			files[p] = text()
			os.MkdirAll(filepath.Dir(filepath.Join(r.Path, p)), 0o755)
			os.WriteFile(filepath.Join(r.Path, p), []byte(files[p]), 0o644)
		}
		time.Sleep(10 * time.Millisecond)
		// The incremental rebuild.
		x.mu.Lock()
		x.dirty = true
		x.mu.Unlock()
		fs, err := x.Fresh(ctx)
		if err != nil {
			t.Fatal(err)
		}
		list := make([]string, len(fs))
		for i, f := range fs {
			list[i] = f.Path
		}
		x.syncText(ctx, list, true)
		x.text.mu.Lock()
		pending := len(x.text.pending)
		x.text.mu.Unlock()
		if pending != 0 {
			t.Fatalf("round %d: %d files still pending after the rebuild", round, pending)
		}
		// A fresh index over the same files.
		fresh := New(r, "")
		waitText(t, fresh)
		for _, q := range queries {
			got, want := answer(x, q), answer(fresh, q)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("round %d, %q: incremental %v\nfresh %v", round, q, got, want)
			}
			superset(t, got, matches(t, r.Path, list, q), q)
		}
	}
}
