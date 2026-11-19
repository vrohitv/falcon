package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/lsp"
	"github.com/vrohitv/falcon/internal/packs"
	"github.com/vrohitv/falcon/internal/repository"
	"github.com/vrohitv/falcon/internal/workspace"
)

// lspRuns remembers how each language server's last request went, so the
// editor can show starting, ready or failed rather than only "installed".
type lspRun struct {
	State string    `json:"state"`
	Note  string    `json:"note"`
	At    time.Time `json:"at"`
}
type lspRuns struct {
	sync.Mutex
	m map[string]lspRun
}

func (l *lspRuns) note(server, state, note string) {
	l.Lock()
	defer l.Unlock()
	if l.m == nil {
		l.m = map[string]lspRun{}
	}
	l.m[server] = lspRun{State: state, Note: domain.Prefix(note, 300), At: time.Now().UTC()}
}

func (l *lspRuns) get(server string) lspRun {
	l.Lock()
	defer l.Unlock()
	return l.m[server]
}

// lspStatus says, per language server, whether it is installed, which one serves each language, and how
// it is doing now: starting, indexing (and on what), ready, or failed.
func (s *Server) lspStatus(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	enabled := !s.NoLSP
	running := map[string]lsp.Status{}
	for _, st := range s.lsps.Statuses() {
		running[st.Command] = st
	}
	servers := []map[string]any{}
	seen := map[string]bool{}
	for _, pack := range packs.Packs {
		chosen, _, installed := pack.ResolveIn(rp.Repo.Path, exec.LookPath)
		if !installed {
			chosen = pack.Servers[0]
		}
		if seen[chosen.Command] {
			continue
		}
		seen[chosen.Command] = true
		languages, extensions := []string{}, []string{}
		for _, p := range packs.Packs {
			if c, _, ok := p.ResolveIn(rp.Repo.Path, exec.LookPath); (ok && c.Command == chosen.Command) || (!ok && p.Servers[0].Command == chosen.Command) {
				languages = append(languages, p.Language)
				extensions = append(extensions, p.Extensions...)
			}
		}
		run := s.lspRuns.get(chosen.Command)
		state, note := run.State, run.Note
		if live, ok := running[chosen.Command]; ok {
			state, note = live.State, live.Doing
		}
		if installed && !chosen.Navigates && note == "" {
			note = "reports problems only; install a navigating server for definitions and references"
		}
		servers = append(servers, map[string]any{"server": chosen.Command, "languages": languages, "extensions": extensions, "installed": installed, "install": chosen.Install, "navigates": chosen.Navigates, "slow": chosen.Slow, "state": state, "note": note, "at": run.At})
	}
	respond(w, map[string]any{"execution": enabled, "host_execution": enabled, "servers": servers})
}

// locations turns a definition or references reply (Location, Location[] or
// LocationLink[]) into repository paths and 1-based lines.
func locations(root string, raw []byte) []map[string]any {
	type position struct {
		Line int `json:"line"`
	}
	type span struct {
		Start position `json:"start"`
	}
	type location struct {
		URI         string `json:"uri"`
		Range       span   `json:"range"`
		TargetURI   string `json:"targetUri"`
		TargetRange span   `json:"targetSelectionRange"`
	}
	var many []location
	if json.Unmarshal(raw, &many) != nil {
		var one location
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		many = []location{one}
	}
	out := []map[string]any{}
	for _, l := range many {
		uri, line := l.URI, l.Range.Start.Line
		if l.TargetURI != "" {
			uri, line = l.TargetURI, l.TargetRange.Start.Line
		}
		u, err := url.Parse(uri)
		if err != nil || u.Scheme != "file" {
			continue
		}
		rel, err := filepath.Rel(root, u.Path)
		if resolved, e := filepath.EvalSymlinks(root); (err != nil || strings.HasPrefix(rel, "..")) && e == nil {
			rel, err = filepath.Rel(resolved, u.Path)
		}
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		out = append(out, map[string]any{"path": filepath.ToSlash(rel), "line": line + 1})
	}
	return out
}

func (s *Server) languages(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	out := []map[string]any{}
	for _, pack := range packs.Packs {
		chosen, _, installed := pack.ResolveIn(rp.Repo.Path, exec.LookPath)
		out = append(out, map[string]any{"pack": pack, "installed": installed, "server": chosen.Command, "navigates": chosen.Navigates})
	}
	respond(w, out)
}

// lspMethods are the questions the editor may ask a language server, and whether answering needs the
// whole project read first (they wait, a while, for indexing to finish).
var lspMethods = map[string]bool{
	"textDocument/definition": true, "textDocument/declaration": true, "textDocument/typeDefinition": true,
	"textDocument/implementation": true, "textDocument/references": true,
	"textDocument/hover": false, "textDocument/documentSymbol": false,
}

// server gives the running language server for a file, started if need be, with the file's text synced.
func (s *Server) server(ctx context.Context, rp *workspace.Repo, path string) (*lsp.Running, packs.Server, string, repository.FileContent, error) {
	if s.NoLSP {
		return nil, packs.Server{}, "", repository.FileContent{}, errors.New("language servers are turned off (-lsp=false)")
	}
	file, err := rp.Repo.Read(path)
	if err != nil {
		return nil, packs.Server{}, "", file, err
	}
	language := repository.Language(path)
	pack, ok := packs.PackFor(language)
	if !ok {
		return nil, packs.Server{}, "", file, errors.New("no language pack for this file")
	}
	chosen, command, ok := pack.ResolveIn(rp.Repo.Path, exec.LookPath)
	if !ok {
		return nil, chosen, "", file, fmt.Errorf("%w (%s): %s", lsp.ErrNoServer, language, pack.Servers[0].Install)
	}
	args := make([]string, len(chosen.Args))
	for i, a := range chosen.Args {
		args[i] = strings.ReplaceAll(a, "{data}", filepath.Join(s.Data, "lsp", chosen.Command))
	}
	if chosen.Slow {
		_ = os.MkdirAll(filepath.Join(s.Data, "lsp", chosen.Command), 0o700)
	}
	if s.lspRuns.get(chosen.Command).State == "" {
		s.lspRuns.note(chosen.Command, "starting", "")
	}
	timeout := 60 * time.Second
	if chosen.Slow {
		timeout = 3 * time.Minute
	}
	running, err := s.lsps.Get(ctx, lsp.Options{Root: rp.Repo.Path, Command: command, Args: args, Env: lsp.Env(), InitTimeout: timeout, InitOptions: initOptions(rp.Repo.Path, chosen.Command, command)})
	if err != nil {
		s.lspRuns.note(chosen.Command, "failed", err.Error())
		return nil, chosen, "", file, err
	}
	uri := lsp.URI(filepath.Join(rp.Repo.Path, path))
	if err = running.Sync(uri, packs.LanguageID(path, language), file.Content); err != nil {
		s.lspRuns.note(chosen.Command, "failed", err.Error())
		return nil, chosen, "", file, err
	}
	return running, chosen, uri, file, nil
}

func (s *Server) lsp(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var input struct {
		Path         string `json:"path"`
		Method       string `json:"method"`
		Line         int    `json:"line"`
		Character    int    `json:"character"`
		ExpectedHash string `json:"expected_hash"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	needsIndex, known := lspMethods[input.Method]
	if !known {
		fail(w, errors.New("unsupported semantic request"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	running, chosen, uri, file, err := s.server(ctx, rp, input.Path)
	if err != nil {
		fail(w, err)
		return
	}
	if file.Hash != input.ExpectedHash && input.ExpectedHash != "" {
		fail(w, errors.New("document changed; reload before requesting semantic information"))
		return
	}
	client := running.Client()
	// Who calls this, where is it implemented: only a server that has read the project knows. Wait for it
	// to finish (a while; a big Java or Rust project can take minutes, and then the answer says so).
	partial := false
	if needsIndex {
		limit := 20 * time.Second
		if chosen.Slow {
			limit = 90 * time.Second
		}
		partial = !client.WaitIdle(ctx, limit)
	}
	params := map[string]any{"textDocument": map[string]string{"uri": uri}}
	if input.Method != "textDocument/documentSymbol" {
		params["position"] = map[string]int{"line": input.Line, "character": input.Character}
	}
	if input.Method == "textDocument/references" {
		params["context"] = map[string]bool{"includeDeclaration": true}
	}
	result, err := client.Call(ctx, input.Method, params)
	if err != nil {
		// A server that does not implement the request is working; it just cannot answer this one.
		if strings.Contains(err.Error(), "-32601") {
			s.lspRuns.note(chosen.Command, "ready", "does not answer "+strings.TrimPrefix(input.Method, "textDocument/")+" requests")
		} else {
			s.lspRuns.note(chosen.Command, "failed", err.Error())
		}
		fail(w, err)
		return
	}
	s.lspRuns.note(chosen.Command, "ready", "")
	current, err := rp.Repo.Read(input.Path)
	if err != nil || current.Hash != file.Hash {
		fail(w, errors.New("stale LSP response rejected after source edit"))
		return
	}
	out := map[string]any{"result": result, "hash": file.Hash, "server": chosen.Command, "partial": partial, "diagnostics": client.Diagnostics(uri)}
	switch input.Method {
	case "textDocument/hover":
		out["hover"] = hoverText(result)
	case "textDocument/documentSymbol":
	default:
		out["locations"] = locations(rp.Repo.Path, result)
	}
	respond(w, out)
}

// lspDiagnostics gives the problems a language server reports for a file: errors, warnings, hints.
func (s *Server) lspDiagnostics(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var input struct {
		Path string `json:"path"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	before := time.Now()
	running, chosen, uri, file, err := s.server(ctx, rp, input.Path)
	if err != nil {
		fail(w, err)
		return
	}
	client := running.Client()
	limit := 4 * time.Second
	if chosen.Slow {
		limit = 15 * time.Second
	}
	var raw json.RawMessage
	if client.PullsDiagnostics() {
		raw, err = client.PullDiagnostics(ctx, uri)
	}
	if raw == nil || err != nil {
		raw = client.WaitDiagnostics(ctx, uri, before, limit)
	}
	var published struct {
		Diagnostics []struct {
			Range struct {
				Start struct{ Line, Character int } `json:"start"`
				End   struct{ Line, Character int } `json:"end"`
			} `json:"range"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Source   string `json:"source"`
			Code     any    `json:"code"`
		} `json:"diagnostics"`
	}
	_ = json.Unmarshal(raw, &published)
	names := map[int]string{1: "error", 2: "warning", 3: "information", 4: "hint"}
	out := []map[string]any{}
	for _, d := range published.Diagnostics {
		sev := names[d.Severity]
		if sev == "" {
			sev = "error"
		}
		out = append(out, map[string]any{"line": d.Range.Start.Line + 1, "character": d.Range.Start.Character, "end_line": d.Range.End.Line + 1, "end_character": d.Range.End.Character, "severity": sev, "message": d.Message, "source": d.Source, "code": d.Code})
	}
	busy, doing := client.Busy()
	respond(w, map[string]any{"path": input.Path, "hash": file.Hash, "server": chosen.Command, "diagnostics": out, "indexing": busy, "doing": doing})
}

// hoverText is a hover reply as Markdown: MarkupContent, a MarkedString, or a list of them.
func hoverText(raw json.RawMessage) string {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return ""
	}
	var markup struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if json.Unmarshal(h.Contents, &markup) == nil && markup.Value != "" {
		if markup.Language != "" {
			return "```" + markup.Language + "\n" + markup.Value + "\n```"
		}
		return markup.Value
	}
	var text string
	if json.Unmarshal(h.Contents, &text) == nil {
		return text
	}
	var many []json.RawMessage
	if json.Unmarshal(h.Contents, &many) == nil {
		parts := []string{}
		for _, m := range many {
			if t := hoverText(json.RawMessage(`{"contents":` + string(m) + `}`)); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// initOptions are what a server needs told at start. typescript-language-server uses the project's own
// TypeScript; a project that has not installed one (a plain JavaScript folder, a fresh clone) gets the
// TypeScript installed beside the server, so navigation works anyway.
func initOptions(root, server, command string) any {
	if server != "typescript-language-server" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", "typescript", "lib", "tsserver.js")); err == nil {
		return nil
	}
	candidates := []string{}
	if real, err := filepath.EvalSymlinks(command); err == nil {
		// …/node_modules/typescript-language-server/lib/cli.mjs → …/node_modules/typescript
		candidates = append(candidates, filepath.Join(filepath.Dir(real), "..", "..", "typescript", "lib", "tsserver.js"))
	}
	// …/node_modules/.bin/typescript-language-server → …/node_modules/typescript
	candidates = append(candidates, filepath.Join(filepath.Dir(command), "..", "typescript", "lib", "tsserver.js"))
	if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
		candidates = append(candidates, filepath.Join(strings.TrimSpace(string(out)), "typescript", "lib", "tsserver.js"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return map[string]any{"tsserver": map[string]string{"path": filepath.Clean(c)}}
		}
	}
	return nil
}
