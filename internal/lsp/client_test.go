package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real child process exercises framing, document synchronization, request
// correlation and cancellation without requiring a particular installed server.
func TestLanguageServerFixture(t *testing.T) {
	if os.Args[len(os.Args)-1] != "lsp-fixture" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	version := 0
	cancelled := false
	for {
		size := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				size, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			}
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(reader, b); err != nil {
			os.Exit(1)
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &msg) != nil {
			os.Exit(2)
		}
		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"positionEncoding": "utf-16", "hoverProvider": true}}
		case "exit":
			os.Exit(0)
		case "textDocument/didOpen", "textDocument/didChange":
			var p struct {
				TextDocument struct {
					Version int `json:"version"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			version = p.TextDocument.Version
		case "$/cancelRequest":
			cancelled = true
		case "test/hang":
			continue
		case "textDocument/hover":
			result = map[string]any{"contents": "fixture hover", "documentVersion": version, "cancelled": cancelled}
		}
		if len(msg.ID) > 0 {
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
			_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(reply), reply)
		}
	}
}

func TestFramedProcessSyncAndCancellation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := StartWith(ctx, Options{Root: t.TempDir(), Command: binary, Args: []string{"-test.run=TestLanguageServerFixture", "--", "lsp-fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	uri := URI("/tmp/spaced file.py")
	for _, source := range []string{"x = 1", "x = 2"} {
		if err = client.Sync(uri, "python", source); err != nil {
			t.Fatal(err)
		}
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	if _, err = client.Call(short, "test/hang", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	response, err := client.Call(ctx, "textDocument/hover", map[string]any{"textDocument": map[string]string{"uri": uri}})
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Contents        string `json:"contents"`
		DocumentVersion int    `json:"documentVersion"`
		Cancelled       bool   `json:"cancelled"`
	}
	if err = json.Unmarshal(response, &data); err != nil {
		t.Fatal(err)
	}
	if data.DocumentVersion != 2 || !data.Cancelled || data.Contents != "fixture hover" {
		t.Fatalf("%s", response)
	}
}

func TestSettingsAnswerConfigurationBySection(t *testing.T) {
	s := map[string]any{"python": map[string]any{"analysis": map[string]any{"typeCheckingMode": "basic"}}, "rust-analyzer": map[string]any{"linkedProjects": []string{"/r/a/Cargo.toml"}}}
	if got := section(s, "python.analysis"); got.(map[string]any)["typeCheckingMode"] != "basic" {
		t.Fatalf("python.analysis: %v", got)
	}
	if got := section(s, "rust-analyzer"); got == nil {
		t.Fatal("rust-analyzer section")
	}
	if got := section(s, "gopls"); got != nil {
		t.Fatalf("a section not given is null: %v", got)
	}
	if got := section(nil, "x.y"); got != nil {
		t.Fatalf("no settings: %v", got)
	}
}
