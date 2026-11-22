package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vrohitv/falcon/internal/packs"
	"github.com/vrohitv/falcon/internal/workspace"
)

// The language features beyond navigation, against a real gopls: highlights of a name in its file, the
// callers and callees of a function, symbols across the folder, and a rename across modules that is
// previewed, then applied to the files (and reaches the index). Runs when gopls and go are installed.
func TestGoplsHighlightsCallsSymbolsAndRename(t *testing.T) {
	gopls, err := packs.Look()("gopls")
	if err != nil {
		t.Skip("gopls is not installed")
	}
	if pack, _ := packs.PackFor("go"); len(pack.Servers[0].Missing(packs.SearchPath(gopls))) > 0 {
		t.Skip("go is not installed")
	}
	dir := repoDir(t, "feat", map[string]string{
		"go.work":           "go 1.22\n\nuse (\n\t./svc\n\t./libs/text\n)\n",
		"svc/go.mod":        "module example.com/svc\n\ngo 1.22\n\nrequire example.com/text v0.0.0\n",
		"svc/api/api.go":    "package api\n\nimport \"example.com/text\"\n\nfunc Handle() string { return text.Upper(\"x\") + text.Upper(\"y\") }\n",
		"libs/text/go.mod":  "module example.com/text\n\ngo 1.22\n",
		"libs/text/text.go": "package text\n\n// Upper is used twice.\nfunc Upper(s string) string { return s }\n",
	})
	ws, err := workspace.Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	rp, err := ws.Add(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	s := New(ws, "", t.TempDir(), t.TempDir())
	t.Cleanup(s.Close)
	h := &harness{t: t, s: s, a: rp}
	ask := func(req map[string]any, out any) {
		t.Helper()
		if p, ok := req["path"].(string); ok {
			b, _ := os.ReadFile(filepath.Join(dir, p))
			sum := sha256.Sum256(b)
			req["expected_hash"] = hex.EncodeToString(sum[:])
		}
		body, _ := json.Marshal(req)
		if code := h.json("POST", "/api/r/"+rp.ID+"/lsp", string(body), out); code != 200 {
			t.Fatalf("%v: %d %+v", req["method"], code, out)
		}
	}

	// Highlights: both uses of Upper in api.go (line 4, 0-based; "Upper" at characters 35 and 53).
	var hl struct {
		Highlights []highlight `json:"highlights"`
	}
	ask(map[string]any{"path": "svc/api/api.go", "method": "textDocument/documentHighlight", "line": 4, "character": 37}, &hl)
	// (gopls also marks the enclosing return's result, as VS Code shows.)
	uppers := 0
	for _, x := range hl.Highlights {
		if x.Line == 5 && (x.Start == 35 || x.Start == 53) && x.End-x.Start == len("Upper") {
			uppers++
		}
	}
	if uppers != 2 {
		t.Fatalf("highlights: %+v", hl.Highlights)
	}

	// Call hierarchy: Upper is called by Handle (twice, on line 5).
	var prep struct {
		Items []callItem `json:"items"`
	}
	ask(map[string]any{"path": "libs/text/text.go", "method": "textDocument/prepareCallHierarchy", "line": 3, "character": 6}, &prep)
	if len(prep.Items) != 1 || prep.Items[0].Name != "Upper" || prep.Items[0].Path != "libs/text/text.go" {
		t.Fatalf("prepare call hierarchy: %+v", prep.Items)
	}
	var in struct {
		Calls []struct {
			Item  callItem `json:"item"`
			Lines []int    `json:"lines"`
		} `json:"calls"`
	}
	ask(map[string]any{"path": "libs/text/text.go", "method": "callHierarchy/incomingCalls", "item": prep.Items[0].Item}, &in)
	if len(in.Calls) != 1 || in.Calls[0].Item.Name != "Handle" || in.Calls[0].Item.Path != "svc/api/api.go" || len(in.Calls[0].Lines) != 2 {
		t.Fatalf("incoming calls: %+v", in.Calls)
	}

	// Symbols across the folder.
	var sym struct {
		Symbols []map[string]any `json:"symbols"`
	}
	ask(map[string]any{"path": "libs/text/text.go", "method": "workspace/symbol", "query": "Handle"}, &sym)
	found := false
	for _, x := range sym.Symbols {
		if x["name"] == "Handle" && x["path"] == "svc/api/api.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("workspace symbols: %+v", sym.Symbols)
	}

	// Rename: prepared (the current name), previewed (both files), applied.
	var pr struct {
		Rename map[string]any `json:"rename"`
	}
	ask(map[string]any{"path": "libs/text/text.go", "method": "textDocument/prepareRename", "line": 3, "character": 6}, &pr)
	if pr.Rename["placeholder"] != "Upper" {
		t.Fatalf("prepare rename: %+v", pr.Rename)
	}
	var rn struct {
		Preview []editPreview `json:"preview"`
		Token   string        `json:"token"`
	}
	ask(map[string]any{"path": "libs/text/text.go", "method": "textDocument/rename", "line": 3, "character": 6, "new_name": "Shout"}, &rn)
	if len(rn.Preview) != 2 || rn.Token == "" {
		t.Fatalf("rename preview: %+v", rn.Preview)
	}
	for _, p := range rn.Preview {
		for _, c := range p.Changes {
			if !strings.Contains(c.After, "Shout") || strings.Contains(c.After, "Upper(") {
				t.Fatalf("preview row: %+v", c)
			}
		}
	}
	var applied struct {
		Changed []string `json:"changed"`
	}
	if code := h.json("POST", "/api/r/"+rp.ID+"/lsp/apply", `{"token":"`+rn.Token+`"}`, &applied); code != 200 || len(applied.Changed) != 2 {
		t.Fatalf("apply: %d %+v", code, applied)
	}
	api, _ := os.ReadFile(filepath.Join(dir, "svc/api/api.go"))
	if strings.Count(string(api), "text.Shout(") != 2 {
		t.Fatalf("api.go after rename:\n%s", api)
	}
	// The token is used up.
	if code := h.json("POST", "/api/r/"+rp.ID+"/lsp/apply", `{"token":"`+rn.Token+`"}`, &applied); code == 200 {
		t.Fatal("a rename token applied twice")
	}
	// gopls sees the renamed code: the old name no longer resolves, the new one does.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var d struct {
			Locations []location `json:"locations"`
		}
		ask(map[string]any{"path": "svc/api/api.go", "method": "textDocument/definition", "line": 4, "character": 37}, &d)
		if len(d.Locations) == 1 && d.Locations[0].Path == "libs/text/text.go" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("definition after rename: %+v", d.Locations)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Edits are made at the right places whatever the characters (UTF-16 positions), last first; overlapping
// edits are refused.
func TestApplyTextEdits(t *testing.T) {
	src := "a😀b = 1\nsecond line\n"
	got, err := applyText(src, []textEdit{
		{Range: lspRange{Start: lspPos{0, 3}, End: lspPos{0, 4}}, NewText: "BEE"}, // after the emoji (2 units)
		{Range: lspRange{Start: lspPos{1, 0}, End: lspPos{1, 6}}, NewText: "2nd"},
		{Range: lspRange{Start: lspPos{0, 0}, End: lspPos{0, 0}}, NewText: "// "},
	})
	if err != nil || got != "// a😀BEE = 1\n2nd line\n" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := applyText(src, []textEdit{{Range: lspRange{Start: lspPos{0, 0}, End: lspPos{0, 5}}}, {Range: lspRange{Start: lspPos{0, 2}, End: lspPos{0, 6}}}}); err == nil {
		t.Fatal("overlapping edits accepted")
	}
	if c := charColumn(src, lspPos{0, 4}); c != 3 {
		t.Fatalf("charColumn: %d", c)
	}
}
