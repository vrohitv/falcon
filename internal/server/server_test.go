package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vrohitv/falcon/internal/workspace"
)

func repoDir(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0o755)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, b)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "Owner")
	run("config", "user.email", "owner@example.test")
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	run("add", "-A")
	run("-c", "commit.gpgSign=false", "commit", "-qm", "first")
	return dir
}

type harness struct {
	t    *testing.T
	s    *Server
	a, b *workspace.Repo
}

func newHarness(t *testing.T, token string) *harness {
	t.Setenv("HOME", t.TempDir())
	ws, err := workspace.Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := ws.Add(repoDir(t, "alpha", map[string]string{"main.go": "package main\n\nfunc Greet() string { return \"hi\" }\n\nfunc main() { Greet() }\n", "docs/readme.md": "Alpha docs\n"}), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ws.Add(repoDir(t, "beta", map[string]string{"app.py": "def hello():\n    return 'beta'\n"}), false)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ws, token, t.TempDir(), t.TempDir())
	s.NoLSP = true
	t.Cleanup(s.Close)
	return &harness{t: t, s: s, a: a, b: b}
}

func (h *harness) do(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:7700"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:7700"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.s.Handler.ServeHTTP(w, r)
	return w
}

func (h *harness) json(method, path, body string, v any) int {
	h.t.Helper()
	w := h.do(method, path, body)
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			h.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
	}
	return w.Code
}

func TestFoldersAreServedSideBySide(t *testing.T) {
	h := newHarness(t, "")
	var list struct {
		Repos []struct{ ID, Name, Branch string }
	}
	h.json("GET", "/api/repos", "", &list)
	if len(list.Repos) != 2 || list.Repos[0].Name != "alpha" || list.Repos[1].Branch != "main" {
		t.Fatalf("%+v", list)
	}
	// The same route reads each folder's own files.
	var fa, fb []struct{ Path string }
	h.json("GET", "/api/r/"+h.a.ID+"/files", "", &fa)
	h.json("GET", "/api/r/"+h.b.ID+"/files", "", &fb)
	if len(fa) != 2 || len(fb) != 1 || fb[0].Path != "app.py" {
		t.Fatalf("%v %v", fa, fb)
	}
	if code := h.json("GET", "/api/r/nope-000000/files", "", nil); code != 404 {
		t.Fatalf("unknown folder: %d", code)
	}
	// Removing one leaves the other.
	if code := h.json("DELETE", "/api/repos/"+h.b.ID, "", nil); code != 200 {
		t.Fatal(code)
	}
	if code := h.json("GET", "/api/r/"+h.b.ID+"/files", "", nil); code != 404 {
		t.Fatalf("removed folder still served: %d", code)
	}
	if code := h.json("GET", "/api/r/"+h.a.ID+"/files", "", nil); code != 200 {
		t.Fatal(code)
	}
}

func TestAddFolderOverHTTP(t *testing.T) {
	h := newHarness(t, "")
	plain := t.TempDir()
	var e struct{ Error string }
	if code := h.json("POST", "/api/repos", `{"path":`+quote(plain)+`}`, &e); code != 400 || !strings.Contains(e.Error, "not a Git repository") {
		t.Fatalf("%d %+v", code, e)
	}
	var r struct{ ID string }
	if code := h.json("POST", "/api/repos", `{"path":`+quote(plain)+`,"init":true}`, &r); code != 200 || r.ID == "" {
		t.Fatalf("%d %+v", code, r)
	}
	var browse struct {
		Path    string
		Folders []struct{ Name string }
	}
	if code := h.json("GET", "/api/browse?path="+filepath.Dir(h.a.Path), "", &browse); code != 200 || len(browse.Folders) != 1 {
		t.Fatalf("%d %+v", code, browse)
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestOnlyLoopbackAndSameOriginWithoutToken(t *testing.T) {
	h := newHarness(t, "")
	r := httptest.NewRequest("GET", "http://evil.example/api/repos", nil)
	w := httptest.NewRecorder()
	h.s.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("foreign host served: %d", w.Code)
	}
	if w := h.do("POST", "/api/repos", `{"path":"/"}`, "Origin", "http://evil.example"); w.Code != 403 {
		t.Fatalf("cross-origin write accepted: %d", w.Code)
	}
	if w := h.do("GET", "/api/repos", "", "Origin", "http://127.0.0.1:7700"); w.Code != 200 {
		t.Fatalf("same origin refused: %d", w.Code)
	}
}

func TestTokenIsRequiredWhenSet(t *testing.T) {
	h := newHarness(t, strings.Repeat("k", 32))
	if w := h.do("GET", "/api/repos", ""); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := h.do("GET", "/api/repos", "", "Authorization", "Bearer "+strings.Repeat("k", 32)); w.Code != 200 {
		t.Fatalf("token refused: %d", w.Code)
	}
	if w := h.do("POST", "/api/login", `{"token":"wrong"}`); w.Code != 401 {
		t.Fatalf("wrong login: %d", w.Code)
	}
	w := h.do("POST", "/api/login", `{"token":"`+strings.Repeat("k", 32)+`"}`)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "falcon_session") {
		t.Fatalf("login: %d %v", w.Code, w.Header())
	}
}

func TestSearchFindsTextWithOptions(t *testing.T) {
	h := newHarness(t, "")
	base := "/api/r/" + h.a.ID + "/search?q="
	var hits []struct {
		Path    string
		Line    int
		Column  int
		Content string
	}
	h.json("GET", base+"greet", "", &hits)
	if len(hits) != 2 || hits[0].Path != "main.go" || hits[0].Line != 3 || hits[0].Column != 6 {
		t.Fatalf("case-insensitive: %+v", hits)
	}
	h.json("GET", base+"greet&case=true", "", &hits)
	if len(hits) != 0 {
		t.Fatalf("case-sensitive: %+v", hits)
	}
	// Match case off means any case, capitals in the query or not (no smart case).
	h.json("GET", base+"GREET", "", &hits)
	if len(hits) != 2 {
		t.Fatalf("capitals with match case off: %+v", hits)
	}
	h.json("GET", base+"Gr%5Be%5D%2Bt%5C(&regex=true", "", &hits)
	if len(hits) != 2 {
		t.Fatalf("regex: %+v", hits)
	}
	h.json("GET", base+"alpha&glob=*.md", "", &hits)
	if len(hits) != 1 || hits[0].Path != "docs/readme.md" {
		t.Fatalf("glob: %+v", hits)
	}
	h.json("GET", base+"alpha&glob=docs/", "", &hits)
	if len(hits) != 1 {
		t.Fatalf("folder glob: %+v", hits)
	}
	// New files are searched too.
	os.WriteFile(filepath.Join(h.a.Path, "fresh.txt"), []byte("Greet from a new file\n"), 0o644)
	h.json("GET", base+"Greet", "", &hits)
	if len(hits) != 3 {
		t.Fatalf("untracked: %+v", hits)
	}
	var e struct{ Error string }
	if code := h.json("GET", base+"(&regex=true", "", &e); code != 400 || e.Error == "" {
		t.Fatalf("bad regex: %d %+v", code, e)
	}
}

func TestViewerEditorAndDefinitions(t *testing.T) {
	h := newHarness(t, "")
	base := "/api/r/" + h.a.ID
	var win struct {
		Total int
		Hash  string
		Lines []string
	}
	h.json("GET", base+"/code?path=main.go", "", &win)
	if win.Total != 6 || win.Hash == "" || !strings.Contains(strings.Join(win.Lines, ""), "Greet") {
		t.Fatalf("%+v", win)
	}
	var defs []struct {
		Path string
		Line int
	}
	h.json("GET", base+"/definition?name=Greet&from=main.go&line=5", "", &defs)
	if len(defs) == 0 || defs[0].Line != 3 {
		t.Fatalf("definition: %+v", defs)
	}
	var outline []struct{ Name string }
	h.json("GET", base+"/outline?path=main.go", "", &outline)
	if len(outline) != 2 {
		t.Fatalf("outline: %+v", outline)
	}
	// Saving needs the hash the editor loaded; a stale one is refused.
	if code := h.json("PUT", base+"/file", `{"path":"main.go","content":"package main\n","expected_hash":"`+win.Hash+`"}`, nil); code != 200 {
		t.Fatal(code)
	}
	if code := h.json("PUT", base+"/file", `{"path":"main.go","content":"lost\n","expected_hash":"`+win.Hash+`"}`, nil); code != 409 {
		t.Fatalf("stale save: %d", code)
	}
	if code := h.json("GET", base+"/file?path=.git/config", "", nil); code == 200 {
		t.Fatal(".git readable")
	}
	if code := h.json("GET", base+"/file?path=../outside", "", nil); code == 200 {
		t.Fatal("path escaped the folder")
	}
}

func TestGitActionsOverHTTPRunOnceAndStageHunks(t *testing.T) {
	h := newHarness(t, "")
	base := "/api/r/" + h.a.ID
	os.WriteFile(filepath.Join(h.a.Path, "main.go"), []byte("package main\n\nfunc Greet() string { return \"hello\" }\n\nfunc main() { Greet() }\n"), 0o644)
	var state struct {
		Revision string
		Files    []struct{ Path, Index, Worktree string }
	}
	h.json("GET", base+"/git/status", "", &state)
	if len(state.Files) != 1 {
		t.Fatalf("%+v", state)
	}
	var diff struct {
		Hunks   []any
		HunkIDs []string `json:"hunk_ids"`
	}
	h.json("GET", base+"/git/diff?area=working&path=main.go", "", &diff)
	if len(diff.Hunks) != 1 || len(diff.HunkIDs) != 1 {
		t.Fatalf("diff: %+v", diff)
	}
	// The same request sent twice at once runs once; both get its result.
	body := `{"id":"op-1","action":"hunk.stage","revision":"` + state.Revision + `","paths":["main.go"],"hunk":"` + diff.HunkIDs[0] + `"}`
	var wg sync.WaitGroup
	results := make([]gitResult, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.json("POST", base+"/git/commands", body, &results[i])
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r.Status != "succeeded" {
			t.Fatalf("%+v", results)
		}
	}
	h.json("GET", base+"/git/status", "", &state)
	if len(state.Files) != 1 || state.Files[0].Index != "M" || state.Files[0].Worktree != "" {
		t.Fatalf("after staging: %+v", state)
	}
	// The ID cannot be reused for something else.
	if code := h.json("POST", base+"/git/commands", `{"id":"op-1","action":"unstage.all","revision":"x"}`, nil); code != 400 {
		t.Fatalf("reused ID: %d", code)
	}
	var res gitResult
	h.json("POST", base+"/git/commands", `{"id":"op-2","action":"commit","revision":"`+state.Revision+`","message":"Say hello"}`, &res)
	if res.Status != "succeeded" || len(res.Result) < 40 {
		t.Fatalf("commit: %+v", res)
	}
	var log struct{ Commits []struct{ Subject string } }
	h.json("GET", base+"/git/log", "", &log)
	if len(log.Commits) != 2 || log.Commits[0].Subject != "Say hello" {
		t.Fatalf("log: %+v", log)
	}
	// A failure is a result with the reason, not an HTTP error.
	h.json("POST", base+"/git/commands", `{"id":"op-3","action":"commit","revision":"stale","message":"x"}`, &res)
	if res.Status != "failed" || !strings.Contains(res.Error, "changed") {
		t.Fatalf("%+v", res)
	}
}

func TestAppRoutesServeTheUI(t *testing.T) {
	h := newHarness(t, "")
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html>falcon"), 0o644)
	h.s.Assets = dist
	for _, p := range []string{"/", "/r/" + h.a.ID + "/", "/r/" + h.a.ID + "/x.go"} {
		w := h.do("GET", p, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "falcon") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Fatalf("%s: %d %s", p, w.Code, w.Header())
		}
	}
	if w := h.do("GET", "/assets/missing.js", ""); w.Code != 404 {
		t.Fatalf("missing asset: %d", w.Code)
	}
	if w := h.do("GET", "/healthz", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}

func TestSymbolSearchFromTheIndex(t *testing.T) {
	h := newHarness(t, "")
	base := "/api/r/" + h.a.ID
	var res struct {
		Ready   bool
		Symbols []struct {
			Name, Kind, Path string
			Line             int
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.json("GET", base+"/symbols?q=gre", "", &res)
		if res.Ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !res.Ready || len(res.Symbols) != 1 || res.Symbols[0].Name != "Greet" || res.Symbols[0].Line != 3 {
		t.Fatalf("%+v", res)
	}
	// Definitions now come from the index, and see a new declaration after a save through Falcon.
	var win struct{ Hash string }
	h.json("GET", base+"/code?path=main.go", "", &win)
	h.json("PUT", base+"/file", `{"path":"extra.go","content":"package main\n\nfunc Wave() {}\n","expected_hash":""}`, nil)
	var defs []struct {
		Path string
		Line int
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		h.json("GET", base+"/definition?name=Wave&from=main.go&line=1", "", &defs)
		if len(defs) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(defs) != 1 || defs[0].Path != "extra.go" {
		t.Fatalf("%+v", defs)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Narrowing while typing returns exactly what a fresh search would.
func TestTypingNarrowsSearchWithoutMissingMatches(t *testing.T) {
	h := newHarness(t, "")
	base := "/api/r/" + h.a.ID + "/search?q="
	writeFile(t, filepath.Join(h.a.Path, "x.txt"), "Greeting\ngreet\nGREET loudly\n")
	type hit struct {
		Path string
		Line int
	}
	run := func(q string) []hit {
		var hits []hit
		h.json("GET", base+q, "", &hits)
		return hits
	}
	run("gre")
	if got := run("greet"); len(got) != 5 {
		t.Fatalf("narrowed lowercase: %+v", got)
	}
	// Capitals do not change anything while Match case is off.
	if got := run("Greet"); len(got) != 5 {
		t.Fatalf("narrowed with capitals: %+v", got)
	}
	// With Match case on, narrowing keeps exactly the case-sensitive matches.
	run("Gre&case=true")
	if got := run("Greet&case=true"); len(got) != 3 { // main.go x2, Greeting
		t.Fatalf("narrowed to case-sensitive: %+v", got)
	}
	run("GRE&case=true")
	if got := run("GREETx&case=true"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got := run("greet%20"); len(got) != 1 { // "GREET loudly"
		t.Fatalf("%+v", got)
	}
	// A file changed through Falcon resets the narrowing.
	run("wave")
	h.json("PUT", "/api/r/"+h.a.ID+"/file", `{"path":"w.txt","content":"waves here\n","expected_hash":""}`, nil)
	if got := run("waves"); len(got) != 1 {
		t.Fatalf("after a save: %+v", got)
	}
}

// Several folders searched through their text indexes: each answers only for itself, changes show up at
// once, and removing a folder drops its index and the copy on disk.
func TestTextIndexesAcrossFolders(t *testing.T) {
	h := newHarness(t, "")
	ida, idb := h.a.ID, h.b.ID
	writeFile(t, filepath.Join(h.a.Path, "shared.txt"), "needle in alpha\n")
	writeFile(t, filepath.Join(h.b.Path, "shared.txt"), "needle in beta\n")
	type hit struct {
		Path    string
		Content string
	}
	search := func(id, q string) []hit {
		var hits []hit
		h.json("GET", "/api/r/"+id+"/search?q="+q, "", &hits)
		return hits
	}
	// Open both folders (starts their indexes) and wait until the text index answers.
	h.json("GET", "/api/r/"+ida+"/summary", "", nil)
	h.json("GET", "/api/r/"+idb+"/summary", "", nil)
	deadline := time.Now().Add(10 * time.Second)
	for !(h.s.index(h.a).TextReady() && h.s.index(h.b).TextReady()) {
		if time.Now().After(deadline) {
			t.Fatal("text indexes not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := search(ida, "needle"); len(got) != 1 || !strings.Contains(got[0].Content, "alpha") {
		t.Fatalf("alpha: %+v", got)
	}
	if got := search(idb, "needle"); len(got) != 1 || !strings.Contains(got[0].Content, "beta") {
		t.Fatalf("beta: %+v", got)
	}
	// A file written by another tool is found on the next search (no event needed).
	writeFile(t, filepath.Join(h.b.Path, "later.txt"), "needle written later\n")
	time.Sleep(800 * time.Millisecond)
	if got := search(idb, "needle"); len(got) != 2 {
		t.Fatalf("after an outside write: %+v", got)
	}
	if got := search(ida, "needle"); len(got) != 1 {
		t.Fatalf("alpha must not see beta's file: %+v", got)
	}
	// Search through the index matches a plain git grep, with file patterns.
	if got := search(ida, "greet&glob=*.go"); len(got) != 2 {
		t.Fatalf("glob through the index: %+v", got)
	}
	if got := search(ida, "nothing-like-this"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// The symbol index is saved on disk; removing the folder deletes it.
	h.s.index(h.b).Refresh(context.Background())
	saved := h.s.indexFile(idb)
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("index not saved: %v", err)
	}
	h.json("DELETE", "/api/repos/"+idb, "", nil)
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatal("removed folder's index still on disk")
	}
	h.s.idxMu.Lock()
	_, kept := h.s.indexes[idb]
	h.s.idxMu.Unlock()
	if kept {
		t.Fatal("removed folder's index still in memory")
	}
}

// A change made right after a page starts listening is reported (the baseline is taken at once).
func TestOutsideChangeRightAfterConnectingIsReported(t *testing.T) {
	h := newHarness(t, "")
	_, ch, err := h.s.watchGit(h.a)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // well before the first poll
	writeFile(t, filepath.Join(h.a.Path, "main.go"), "package main\n// edited outside\n")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the change was never reported")
	}
}

// Git reading its own index (every status does) is not a change: no event, so no refresh loop.
func TestReadingTheRepositoryIsNotAChange(t *testing.T) {
	h := newHarness(t, "")
	_, ch, err := h.s.watchGit(h.a)
	if err != nil {
		t.Fatal(err)
	}
	// Drain anything from setting up.
	time.Sleep(300 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}
	for i := 0; i < 5; i++ {
		h.json("GET", "/api/r/"+h.a.ID+"/summary", "", nil)
		h.json("GET", "/api/r/"+h.a.ID+"/git/status", "", nil)
		time.Sleep(100 * time.Millisecond)
	}
	select {
	case n := <-ch:
		t.Fatalf("reading fired a change event (%d)", n)
	case <-time.After(2 * time.Second):
	}
}
