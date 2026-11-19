package lsp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A pool keeps one server per command and root: asked twice, it is the same process, and text that has
// not changed is not sent again.
func TestPoolKeepsOneServerAndSyncsOnlyChanges(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(time.Hour)
	defer pool.Close()
	o := Options{Root: t.TempDir(), Command: binary, Args: []string{"-test.run=TestLanguageServerFixture", "--", "lsp-fixture"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := pool.Get(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pool.Get(ctx, o)
	if err != nil || a != b {
		t.Fatalf("a second Get started another server: %v", err)
	}
	if err = a.Sync("file:///x.go", "go", "package x"); err != nil {
		t.Fatal(err)
	}
	if err = a.Sync("file:///x.go", "go", "package x"); err != nil {
		t.Fatal(err)
	}
	a.client.mu.Lock()
	v := a.client.versions["file:///x.go"]
	a.client.mu.Unlock()
	if v != 1 {
		t.Fatalf("unchanged text was sent again: version %d", v)
	}
	st := pool.Statuses()
	if len(st) != 1 || st[0].State != "ready" || st[0].Docs != 1 {
		t.Fatalf("%+v", st)
	}
	if busy, _ := a.client.Busy(); busy {
		t.Fatal("busy without any progress")
	}
	a.client.notice("$/progress", []byte(`{"token":"t","value":{"kind":"begin","title":"Indexing"}}`))
	if busy, what := a.client.Busy(); !busy || !strings.Contains(what, "Indexing") {
		t.Fatal("progress begun is not seen")
	}
	a.client.notice("$/progress", []byte(`{"token":"t","value":{"kind":"end"}}`))
	if !a.client.WaitIdle(ctx, 2*time.Second) {
		t.Fatal("progress ended but still busy")
	}
	pool.Close()
	time.Sleep(300 * time.Millisecond)
	if a.client.Alive() {
		t.Fatal("Close left the server running")
	}
}

func TestEnvPassesToolLocationsAndNotSecrets(t *testing.T) {
	t.Setenv("GOMODCACHE", "/mods")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	env := strings.Join(Env(), "\n")
	if !strings.Contains(env, "GOMODCACHE=/mods") || strings.Contains(env, "secret") {
		t.Fatalf("%s", env)
	}
}
