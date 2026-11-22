package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type wsClient struct {
	t    *testing.T
	c    *websocket.Conn
	text chan map[string]any
	bin  chan []byte
}

func dial(t *testing.T, srv *httptest.Server, id string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/r/" + id + "/ws"
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(8 << 20)
	w := &wsClient{t: t, c: c, text: make(chan map[string]any, 256), bin: make(chan []byte, 1024)}
	go func() {
		for {
			kind, data, err := c.Read(context.Background())
			if err != nil {
				close(w.text)
				return
			}
			if kind == websocket.MessageBinary {
				w.bin <- data
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			w.text <- m
		}
	}()
	t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "") })
	return w
}

func (w *wsClient) send(v any) {
	b, _ := json.Marshal(v)
	if err := w.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsClient) expect(kind string, within time.Duration) map[string]any {
	w.t.Helper()
	deadline := time.After(within)
	for {
		select {
		case m, ok := <-w.text:
			if !ok {
				w.t.Fatalf("closed while waiting for %s", kind)
			}
			if m["t"] == kind {
				return m
			}
		case <-deadline:
			w.t.Fatalf("no %s message", kind)
		}
	}
}

// output collects a terminal's frames (acknowledging them) until they contain want.
func (w *wsClient) output(id, want string, within time.Duration) string {
	w.t.Helper()
	var got strings.Builder
	deadline := time.After(within)
	for {
		select {
		case f := <-w.bin:
			n := int(f[0])
			if string(f[1:1+n]) != id {
				continue
			}
			got.Write(f[1+n:])
			w.send(map[string]any{"t": "term.ack", "id": id, "bytes": len(f) - 1 - n})
			if strings.Contains(got.String(), want) {
				return got.String()
			}
		case <-deadline:
			w.t.Fatalf("waited for %q in the terminal; got %q", want, got.String())
		}
	}
}

func (w *wsClient) typeIn(id, text string) {
	frame := append([]byte{byte(len(id))}, id...)
	frame = append(frame, text...)
	if err := w.c.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
		w.t.Fatal(err)
	}
}

func TestOneSocketCarriesGitFilesAndTerminals(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	h := newHarness(t, "")
	h.s.Terminal = true
	srv := httptest.NewServer(h.s.Handler)
	t.Cleanup(srv.Close)
	ws := dial(t, srv, h.a.ID)
	if hello := ws.expect("hello", 3*time.Second); hello["terminal"] != true {
		t.Fatalf("hello: %v", hello)
	}
	// File changes: the watched file's state at once, then each change.
	ws.send(map[string]any{"t": "watch", "paths": []string{"main.go"}})
	first := ws.expect("file", 3*time.Second)
	if first["path"] != "main.go" || first["exists"] != true {
		t.Fatalf("file: %v", first)
	}
	os.WriteFile(filepath.Join(h.a.Repo.Path, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	if changed := ws.expect("file", 5*time.Second); changed["hash"] == first["hash"] {
		t.Fatalf("the change was not reported: %v", changed)
	}
	// Git changes: a commit made in a terminal elsewhere.
	cmd := exec.Command("git", "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-qam", "change")
	cmd.Dir = h.a.Repo.Path
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s", b)
	}
	ws.expect("git", 8*time.Second)
	// A terminal: open, type, read.
	ws.send(map[string]any{"t": "term.open", "req": "r1", "cols": 100, "rows": 30})
	opened := ws.expect("term.opened", 5*time.Second)
	if opened["req"] != "r1" {
		t.Fatalf("opened: %v", opened)
	}
	id := opened["session"].(map[string]any)["id"].(string)
	ws.expect("term.attached", 3*time.Second)
	line, size, enter := "echo sum-$((20+22)); stty size", "30 100", "\n"
	if runtime.GOOS == "windows" {
		line, size, enter = `echo "sum-$(20+22)"; $z = $Host.UI.RawUI.WindowSize; echo "size-$($z.Height)x$($z.Width)"`, "size-30x100", "\r\n"
	}
	ws.typeIn(id, line+enter)
	out := ws.output(id, size, 10*time.Second)
	if !strings.Contains(out, "sum-42") {
		t.Fatalf("terminal output: %q", out)
	}
	// The page reloads: a new connection attaches to the same session and gets its screen back.
	ws.c.Close(websocket.StatusNormalClosure, "")
	again := dial(t, srv, h.a.ID)
	again.expect("hello", 3*time.Second)
	var list struct {
		Enabled  bool `json:"enabled"`
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	h.json("GET", "/api/r/"+h.a.ID+"/terminals", "", &list)
	if !list.Enabled || len(list.Sessions) != 1 || list.Sessions[0].ID != id {
		t.Fatalf("terminals: %+v", list)
	}
	again.send(map[string]any{"t": "term.attach", "id": id, "cols": 100, "rows": 30})
	again.expect("term.attached", 3*time.Second)
	if back := again.output(id, "sum-42", 5*time.Second); !strings.Contains(back, "sum-42") {
		t.Fatal("the screen did not come back")
	}
	// Another folder cannot reach this session.
	other := dial(t, srv, h.b.ID)
	other.expect("hello", 3*time.Second)
	other.send(map[string]any{"t": "term.attach", "id": id})
	other.expect("term.gone", 3*time.Second)
	// Exit is reported, and the session is gone.
	again.typeIn(id, "exit 7"+enter)
	if exit := again.expect("term.exit", 5*time.Second); exit["code"] != float64(7) {
		t.Fatalf("exit: %v", exit)
	}
	again.send(map[string]any{"t": "term.attach", "id": id})
	again.expect("term.gone", 3*time.Second)
}

func TestSocketRefusesOtherOriginsAndTerminalsCanBeOff(t *testing.T) {
	h := newHarness(t, "")
	srv := httptest.NewServer(h.s.Handler)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/r/" + h.a.ID + "/ws"
	if _, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("a page from another site must not connect: %v %v", err, resp)
	}
	h.s.Terminal = false
	ws := dial(t, srv, h.a.ID)
	if hello := ws.expect("hello", 3*time.Second); hello["terminal"] != false {
		t.Fatalf("hello: %v", hello)
	}
	ws.send(map[string]any{"t": "term.open", "req": "x", "cols": 80, "rows": 24})
	if e := ws.expect("error", 3*time.Second); !strings.Contains(e["message"].(string), "turned off") {
		t.Fatalf("error: %v", e)
	}
}

// Changing the watched files (every tab opened or closed) must not leave a goroutine behind each time.
func TestReplacingWatchedFilesLeavesNothingBehind(t *testing.T) {
	h := newHarness(t, "")
	srv := httptest.NewServer(h.s.Handler)
	t.Cleanup(srv.Close)
	ws := dial(t, srv, h.a.ID)
	ws.expect("hello", 3*time.Second)
	ws.send(map[string]any{"t": "watch", "paths": []string{"main.go"}})
	ws.expect("file", 3*time.Second)
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		paths := []string{"main.go"}
		if i%2 == 0 {
			paths = append(paths, "docs/readme.md")
		}
		ws.send(map[string]any{"t": "watch", "paths": paths})
	}
	ws.send(map[string]any{"t": "watch", "paths": []string{"main.go"}})
	time.Sleep(500 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Fatalf("goroutines grew from %d to %d after replacing the watched files 100 times", before, after)
	}
}
