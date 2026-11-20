package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vrohitv/falcon/internal/packs"
	"github.com/vrohitv/falcon/internal/workspace"
)

// One gopls serves a whole monorepo (two modules joined by go.work): definitions and references cross
// modules, and a file created on disk afterwards reaches it. Runs when gopls and go are installed.
func TestGoplsServesTheWholeMonorepo(t *testing.T) {
	gopls, err := packs.Look()("gopls")
	if err != nil {
		t.Skip("gopls is not installed")
	}
	if pack, _ := packs.PackFor("go"); len(pack.Servers[0].Missing(packs.SearchPath(gopls))) > 0 {
		t.Skip("go is not installed")
	}
	dir := repoDir(t, "mono", map[string]string{
		"go.work":           "go 1.22\n\nuse (\n\t./svc\n\t./libs/text\n)\n",
		"svc/go.mod":        "module example.com/svc\n\ngo 1.22\n\nrequire example.com/text v0.0.0\n",
		"svc/api/api.go":    "package api\n\nimport \"example.com/text\"\n\nfunc Handle() string { return text.Upper(\"x\") }\n\nfunc Uses() string { return Later() }\n",
		"libs/text/go.mod":  "module example.com/text\n\ngo 1.22\n",
		"libs/text/text.go": "package text\n\nfunc Upper(s string) string { return s }\n",
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
	ask := func(path, method string, line, char int) []string {
		b, _ := os.ReadFile(filepath.Join(dir, path))
		sum := sha256.Sum256(b)
		body, _ := json.Marshal(map[string]any{"path": path, "method": "textDocument/" + method, "line": line, "character": char, "expected_hash": hex.EncodeToString(sum[:])})
		var out struct {
			Locations []location `json:"locations"`
			Error     string     `json:"error"`
		}
		h.json("POST", "/api/r/"+rp.ID+"/lsp", string(body), &out)
		if out.Error != "" {
			t.Fatalf("%s %s: %s", method, path, out.Error)
		}
		got := []string{}
		for _, l := range out.Locations {
			got = append(got, l.Path+":"+itoa(l.Line))
		}
		sort.Strings(got)
		return got
	}
	if got := ask("svc/api/api.go", "definition", 4, 36); strings.Join(got, ",") != "libs/text/text.go:3" {
		t.Fatalf("definition across modules: %v", got)
	}
	if got := ask("libs/text/text.go", "references", 2, 6); strings.Join(got, ",") != "libs/text/text.go:3,svc/api/api.go:5" {
		t.Fatalf("references across modules: %v", got)
	}
	// A file created on disk after the server started (a terminal, a branch switch) reaches it.
	if got := ask("svc/api/api.go", "definition", 6, 28); len(got) != 0 {
		t.Fatalf("Later does not exist yet: %v", got)
	}
	os.WriteFile(filepath.Join(dir, "svc/api/later.go"), []byte("package api\n\nfunc Later() string { return \"later\" }\n"), 0o644)
	s.reportChanges(rp)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if got := ask("svc/api/api.go", "definition", 6, 28); strings.Join(got, ",") == "svc/api/later.go:3" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("a new file did not reach gopls: %v", got)
		}
		time.Sleep(300 * time.Millisecond)
	}
	// One server for the whole folder.
	var st struct {
		Servers []struct {
			Server    string `json:"server"`
			Instances []struct {
				Root string `json:"root"`
			} `json:"instances"`
		} `json:"servers"`
	}
	h.json("GET", "/api/r/"+rp.ID+"/lsp/status", "", &st)
	for _, sv := range st.Servers {
		if sv.Server == "gopls" && (len(sv.Instances) != 1 || sv.Instances[0].Root != ".") {
			t.Fatalf("gopls instances: %+v", sv.Instances)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
