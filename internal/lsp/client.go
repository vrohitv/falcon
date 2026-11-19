// Package lsp implements framed JSON-RPC with request correlation, cancellation,
// negotiated UTF-16 positions and independent process lifetime per workspace.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vrohitv/falcon/internal/sysenv"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type response struct {
	Result json.RawMessage
	Error  error
}
type Client struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	writeMu      sync.Mutex
	mu           sync.Mutex
	pending      map[int64]chan response
	diagnostics  map[string]json.RawMessage
	versions     map[string]int
	next         atomic.Int64
	done         chan struct{}
	home         string
	closeOnce    sync.Once
	Capabilities json.RawMessage
	// Work the server says it is doing ($/progress tokens begun and not ended), and whether it says it is
	// quiescent (rust-analyzer's experimental/serverStatus). Together: is it still indexing.
	progress  map[string]string
	quiescent *bool
	diagAt    map[string]time.Time
	exited    atomic.Bool
}

// Options start a language server: where, what, and with which environment and initialization options.
type Options struct {
	Root    string
	Command string
	Args    []string
	// Env is the server's whole environment. Empty: a minimal one with a private HOME (as before).
	Env         []string
	InitOptions any
	InitTimeout time.Duration
}

func URI(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

// clientCapabilities are what Falcon's editor can use: navigation, hover in Markdown, hierarchical symbols,
// diagnostics, and progress reports (so it knows when a server is still indexing).
var clientCapabilities = map[string]any{
	"general":   map[string]any{"positionEncodings": []string{"utf-16"}},
	"window":    map[string]any{"workDoneProgress": true},
	"workspace": map[string]any{"configuration": true, "workspaceFolders": true},
	"textDocument": map[string]any{
		"synchronization":    map[string]any{"dynamicRegistration": false, "didSave": true},
		"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
		"definition":         map[string]any{"linkSupport": true},
		"declaration":        map[string]any{"linkSupport": true},
		"typeDefinition":     map[string]any{"linkSupport": true},
		"implementation":     map[string]any{"linkSupport": true},
		"references":         map[string]any{},
		"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
		"publishDiagnostics": map[string]any{"relatedInformation": true, "versionSupport": true},
		"diagnostic":         map[string]any{"dynamicRegistration": false, "relatedDocumentSupport": false},
	},
	// rust-analyzer says when it has finished loading the workspace.
	"experimental": map[string]any{"serverStatusNotification": true},
}

// StartWith starts a language server and completes the LSP handshake.
func StartWith(ctx context.Context, o Options) (*Client, error) {
	root, command, args := o.Root, o.Command, o.Args
	home, err := os.MkdirTemp("", "falcon-lsp-")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = root
	cmd.Env = o.Env
	if len(cmd.Env) == 0 {
		cmd.Env = append(sysenv.Base(), "HOME="+home, "TMPDIR="+home)
	}
	ownGroup(cmd)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(home)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(home)
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		os.RemoveAll(home)
		return nil, err
	}
	c := &Client{cmd: cmd, stdin: stdin, pending: map[int64]chan response{}, diagnostics: map[string]json.RawMessage{}, versions: map[string]int{}, done: make(chan struct{}), home: home, progress: map[string]string{}, diagAt: map[string]time.Time{}}
	go c.read(stdout)
	timeout := o.InitTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	params := map[string]any{"processId": os.Getpid(), "rootUri": URI(root), "rootPath": root, "workspaceFolders": []any{map[string]string{"uri": URI(root), "name": "repository"}}, "capabilities": clientCapabilities}
	if o.InitOptions != nil {
		params["initializationOptions"] = o.InitOptions
	}
	result, err := c.Call(initCtx, "initialize", params)
	if err != nil {
		c.Close()
		return nil, err
	}
	var initialized struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err = json.Unmarshal(result, &initialized); err != nil {
		c.Close()
		return nil, fmt.Errorf("language server initialization: %w", err)
	}
	var capabilities struct {
		PositionEncoding string `json:"positionEncoding"`
	}
	if err = json.Unmarshal(initialized.Capabilities, &capabilities); err != nil {
		c.Close()
		return nil, fmt.Errorf("language server capabilities: %w", err)
	}
	if capabilities.PositionEncoding != "" && capabilities.PositionEncoding != "utf-16" {
		c.Close()
		return nil, errors.New("language server did not negotiate UTF-16 positions")
	}
	c.Capabilities = initialized.Capabilities
	if err = c.Notify("initialized", map[string]any{}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(b), b)
	return err
}
func (c *Client) Notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.next.Add(1)
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.Result, r.Error
	case <-ctx.Done():
		_ = c.Notify("$/cancelRequest", map[string]int64{"id": id})
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("language server exited")
	}
}
func (c *Client) read(reader io.Reader) {
	defer close(c.done)
	defer c.exited.Store(true)
	r := bufio.NewReader(reader)
	for {
		size := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				size, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
			}
		}
		if size <= 0 || size > 16<<20 {
			return
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &msg) != nil {
			return
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				var result any
				switch msg.Method {
				case "workspace/configuration":
					var p struct {
						Items []any `json:"items"`
					}
					_ = json.Unmarshal(msg.Params, &p)
					result = make([]any, len(p.Items))
				case "workspace/applyEdit":
					result = map[string]any{"applied": false, "failureReason": "Edits must pass through the repository broker"}
				case "workspace/workspaceFolders":
					result = []any{}
				}
				_ = c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
			} else {
				c.notice(msg.Method, msg.Params)
			}
			continue
		}
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			out := response{Result: msg.Result}
			if len(msg.Error) > 0 && string(msg.Error) != "null" {
				out.Error = fmt.Errorf("language server: %s", msg.Error)
			}
			select {
			case ch <- out:
			default:
			}
		}
	}
}

// notice takes in what the server says without being asked: diagnostics, and how far along its work is.
func (c *Client) notice(method string, params json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch method {
	case "textDocument/publishDiagnostics":
		var p struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(params, &p) == nil {
			c.diagnostics[p.URI] = params
			c.diagAt[p.URI] = time.Now()
		}
	case "$/progress":
		var p struct {
			Token any `json:"token"`
			Value struct {
				Kind    string `json:"kind"`
				Title   string `json:"title"`
				Message string `json:"message"`
			} `json:"value"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		token := fmt.Sprint(p.Token)
		switch p.Value.Kind {
		case "begin":
			c.progress[token] = strings.TrimSpace(p.Value.Title + " " + p.Value.Message)
		case "report":
			if _, ok := c.progress[token]; ok && p.Value.Message != "" {
				c.progress[token] = p.Value.Message
			}
		case "end":
			delete(c.progress, token)
		}
	case "experimental/serverStatus":
		var p struct {
			Quiescent bool `json:"quiescent"`
		}
		if json.Unmarshal(params, &p) == nil {
			c.quiescent = &p.Quiescent
		}
	}
}

// Busy reports whether the server says it is still working (indexing, loading the workspace), and on what.
func (c *Client) Busy() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, what := range c.progress {
		return true, what
	}
	if c.quiescent != nil && !*c.quiescent {
		return true, "loading the workspace"
	}
	return false, ""
}

// WaitIdle waits until the server is not busy, `limit` at most. False when it was still busy.
func (c *Client) WaitIdle(ctx context.Context, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	// A server that has just been told about a file often begins its work a moment later.
	settle := time.Now().Add(300 * time.Millisecond)
	for {
		busy, _ := c.Busy()
		if !busy && time.Now().After(settle) {
			return true
		}
		if time.Now().After(deadline) || c.exited.Load() {
			return !busy
		}
		select {
		case <-ctx.Done():
			return false
		case <-c.done:
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// WaitDiagnostics waits for diagnostics for `uri` published after `since`, `limit` at most, and gives
// whatever the server has for it then.
func (c *Client) WaitDiagnostics(ctx context.Context, uri string, since time.Time, limit time.Duration) json.RawMessage {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		at := c.diagAt[uri]
		c.mu.Unlock()
		if at.After(since) {
			break
		}
		select {
		case <-ctx.Done():
			return c.Diagnostics(uri)
		case <-c.done:
			return c.Diagnostics(uri)
		case <-time.After(60 * time.Millisecond):
		}
	}
	return c.Diagnostics(uri)
}

// Alive reports whether the server process is still running.
func (c *Client) Alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *Client) Sync(uri, language, text string) error {
	c.mu.Lock()
	v := c.versions[uri] + 1
	c.versions[uri] = v
	c.mu.Unlock()
	if v == 1 {
		return c.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": v, "text": text}})
	}
	return c.Notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": v}, "contentChanges": []any{map[string]string{"text": text}}})
}
func (c *Client) Diagnostics(uri string) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.diagnostics[uri]; d != nil {
		return d
	}
	return json.RawMessage(`{"diagnostics":[]}`)
}
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = c.Call(ctx, "shutdown", nil)
		cancel()
		_ = c.Notify("exit", nil)
		killTree(c.cmd)
		_ = c.stdin.Close()
		_ = c.cmd.Wait()
		_ = os.RemoveAll(c.home)
	})
}

// PullsDiagnostics reports whether the server answers textDocument/diagnostic (LSP 3.17) instead of, or as
// well as, publishing diagnostics by itself. TypeScript 7 only answers when asked.
func (c *Client) PullsDiagnostics() bool {
	var caps struct {
		DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
	}
	return json.Unmarshal(c.Capabilities, &caps) == nil && len(caps.DiagnosticProvider) > 0 && string(caps.DiagnosticProvider) != "null" && string(caps.DiagnosticProvider) != "false"
}

// PullDiagnostics asks for a document's diagnostics, and gives them as a publishDiagnostics would.
func (c *Client) PullDiagnostics(ctx context.Context, uri string) (json.RawMessage, error) {
	raw, err := c.Call(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]string{"uri": uri}})
	if err != nil {
		return nil, err
	}
	var report struct {
		Kind  string          `json:"kind"`
		Items json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &report) != nil || len(report.Items) == 0 || string(report.Items) == "null" {
		return json.RawMessage(`{"diagnostics":[]}`), nil
	}
	return json.RawMessage(`{"uri":` + strconvQuote(uri) + `,"diagnostics":` + string(report.Items) + `}`), nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
