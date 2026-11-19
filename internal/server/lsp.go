package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/lsp"
	"github.com/vrohitv/falcon/internal/packs"
	"github.com/vrohitv/falcon/internal/repository"
	"github.com/vrohitv/falcon/internal/workspace"
)

// lspRuns remembers how each language server's last request went, per project root (one folder can hold
// several projects, and several folders are served at once), so the editor shows starting, ready or
// failed for this folder's servers, not another's.
type lspRun struct {
	State string    `json:"state"`
	Note  string    `json:"note"`
	At    time.Time `json:"at"`
}
type lspRuns struct {
	sync.Mutex
	m map[string]lspRun
}

func runKey(root, server string) string { return root + "\x00" + server }

func (l *lspRuns) note(root, server, state, note string) {
	l.Lock()
	defer l.Unlock()
	if l.m == nil {
		l.m = map[string]lspRun{}
	}
	l.m[runKey(root, server)] = lspRun{State: state, Note: domain.Prefix(note, 300), At: time.Now().UTC()}
}

// latest is the most recent run of a server anywhere in a folder.
func (l *lspRuns) latest(folder, server string) lspRun {
	l.Lock()
	defer l.Unlock()
	var best lspRun
	for k, r := range l.m {
		root, cmd, _ := strings.Cut(k, "\x00")
		if cmd == server && within(folder, root) && r.At.After(best.At) {
			best = r
		}
	}
	return best
}

func within(folder, path string) bool {
	rel, err := filepath.Rel(folder, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// lspStatus says, per language server, whether it is installed and where it was found, which languages
// it serves, how to install it on this system (and where that puts it), and how its instances for this
// folder are doing: starting, indexing (and on what), ready, or failed.
func (s *Server) lspStatus(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	enabled := !s.NoLSP
	look := packs.Look(rp.Repo.Path)
	type instance struct {
		Root  string `json:"root"`
		State string `json:"state"`
		Doing string `json:"doing,omitempty"`
		Docs  int    `json:"documents"`
	}
	running := map[string][]instance{}
	for _, st := range s.lsps.Statuses() {
		if !within(rp.Repo.Path, st.Root) {
			continue
		}
		rel, _ := filepath.Rel(rp.Repo.Path, st.Root)
		running[filepath.Base(st.Command)] = append(running[filepath.Base(st.Command)], instance{Root: filepath.ToSlash(rel), State: st.State, Doing: st.Doing, Docs: st.Docs})
	}
	servers := []map[string]any{}
	seen := map[string]bool{}
	for _, pack := range packs.Packs {
		chosen, found, installed := pack.ResolveIn(rp.Repo.Path, look)
		if !installed {
			chosen = pack.Servers[0]
		}
		if seen[chosen.Command] {
			continue
		}
		seen[chosen.Command] = true
		languages, extensions := []string{}, []string{}
		for _, p := range packs.Packs {
			if c, _, ok := p.ResolveIn(rp.Repo.Path, look); (ok && c.Command == chosen.Command) || (!ok && p.Servers[0].Command == chosen.Command) {
				languages = append(languages, p.Language)
				extensions = append(extensions, p.Extensions...)
			}
		}
		run := s.lspRuns.latest(rp.Repo.Path, chosen.Command)
		state, note := run.State, run.Note
		live := running[filepath.Base(found)]
		if found == "" {
			live = nil
		}
		// The folder's instances together: one still starting or reading the project shows first.
		for _, rank := range []string{"failed", "starting", "indexing", "ready", "exited"} {
			for _, in := range live {
				if in.State == rank && (state == "" || rank != "exited") {
					state, note = in.State, in.Doing
					goto chosenState
				}
			}
		}
	chosenState:
		if installed && !chosen.Navigates && note == "" {
			note = "reports problems only; install a navigating server for definitions and references"
		}
		alternatives := []map[string]any{}
		for _, other := range pack.Servers {
			if other.Command != chosen.Command {
				alternatives = append(alternatives, map[string]any{"server": other.Command, "installs": other.InstallsFor(runtime.GOOS), "navigates": other.Navigates})
			}
		}
		servers = append(servers, map[string]any{
			"server": chosen.Command, "languages": languages, "extensions": extensions, "installed": installed,
			"path": found, "install": chosen.Install, "installs": chosen.InstallsFor(runtime.GOOS), "alternatives": alternatives,
			"navigates": chosen.Navigates, "slow": chosen.Slow, "state": state, "note": note, "at": run.At, "instances": live,
		})
	}
	searched := append(packs.ProjectDirs(rp.Repo.Path), filepath.SplitList(os.Getenv("PATH"))...)
	searched = append(searched, packs.ToolDirs()...)
	respond(w, map[string]any{"execution": enabled, "host_execution": enabled, "os": runtime.GOOS, "servers": servers, "searched": searched})
}

// location is a place a language server answered with, as the editor shows it: a path in the folder (or
// an ~ext/ path for a file outside it), the 1-based line, the column and end column (characters), and the
// line's text for a preview.
type location struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	End      int    `json:"end"`
	Text     string `json:"text,omitempty"`
	External string `json:"external,omitempty"`
}

// locations turns a definition or references reply (Location, Location[] or LocationLink[]) into places
// the editor can open. Columns arrive in UTF-16 units and leave as characters.
func (s *Server) locations(rp *workspace.Repo, raw []byte) []location {
	type position struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}
	type span struct {
		Start position `json:"start"`
		End   position `json:"end"`
	}
	type item struct {
		URI         string `json:"uri"`
		Range       span   `json:"range"`
		TargetURI   string `json:"targetUri"`
		TargetRange span   `json:"targetSelectionRange"`
	}
	var many []item
	if json.Unmarshal(raw, &many) != nil {
		var one item
		if json.Unmarshal(raw, &one) != nil || (one.URI == "" && one.TargetURI == "") {
			return []location{}
		}
		many = []item{one}
	}
	root := rp.Repo.Path
	resolved, _ := filepath.EvalSymlinks(root)
	lines := map[string][]string{}
	out := []location{}
	seen := map[string]bool{}
	for _, l := range many {
		uri, rng := l.URI, l.Range
		if l.TargetURI != "" {
			uri, rng = l.TargetURI, l.TargetRange
		}
		abs := lsp.FromURI(uri)
		if abs == "" {
			continue
		}
		loc := location{Line: rng.Start.Line + 1}
		rel, err := filepath.Rel(root, abs)
		if (err != nil || strings.HasPrefix(rel, "..")) && resolved != "" {
			rel, err = filepath.Rel(resolved, abs)
		}
		if err == nil && !strings.HasPrefix(rel, "..") {
			loc.Path = filepath.ToSlash(rel)
		} else if loc.Path = s.external.add(rp.ID, abs); loc.Path == "" {
			continue
		} else {
			loc.External = abs
		}
		if seen[loc.Path+"\x00"+fmt.Sprint(loc.Line, rng.Start.Character)] {
			continue
		}
		seen[loc.Path+"\x00"+fmt.Sprint(loc.Line, rng.Start.Character)] = true
		text, ok := lines[abs]
		if !ok {
			if b, err := os.ReadFile(abs); err == nil && len(b) < 8<<20 {
				text = strings.Split(string(b), "\n")
			}
			lines[abs] = text
		}
		if rng.Start.Line < len(text) {
			line := strings.TrimSuffix(text[rng.Start.Line], "\r")
			loc.Column = characters(line, rng.Start.Character)
			loc.End = loc.Column
			if rng.End.Line == rng.Start.Line {
				loc.End = characters(line, rng.End.Character)
			}
			loc.Text = domain.Prefix(line, 400)
		}
		out = append(out, loc)
	}
	return out
}

// characters converts a UTF-16 column (the LSP's) to a character column in line.
func characters(line string, utf16col int) int {
	units, chars := 0, 0
	for _, r := range line {
		if units >= utf16col {
			break
		}
		units += len(utf16.Encode([]rune{r}))
		chars++
	}
	return chars
}

func (s *Server) languages(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	out := []map[string]any{}
	look := packs.Look(rp.Repo.Path)
	for _, pack := range packs.Packs {
		chosen, _, installed := pack.ResolveIn(rp.Repo.Path, look)
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

// served is a language server ready for one file: the running server, which one it is, the project root
// it runs at, the file's URI and its contents.
type served struct {
	running *lsp.Running
	chosen  packs.Server
	root    string
	uri     string
	file    repository.FileContent
}

// server gives the running language server for a file, started if need be, with the file's text synced.
// It runs at the file's project root (found from go.mod, Cargo.toml, package.json and the like), with a
// PATH that holds the folder it was found in and the project's own tools. A file outside the folder (an
// ~ext/ path) uses the server of the project it was reached from.
func (s *Server) server(ctx context.Context, rp *workspace.Repo, path, from string) (served, error) {
	var sv served
	if s.NoLSP {
		return sv, errors.New("language servers are turned off (-lsp=false)")
	}
	file, abs, err := s.readContent(rp, path)
	if err != nil {
		return sv, err
	}
	sv.file = file
	language := repository.Language(path)
	pack, ok := packs.PackFor(language)
	if !ok {
		return sv, errors.New("no language server serves this kind of file")
	}
	// The project: the file's own, or for an outside file the one it was reached from.
	at := path
	if strings.HasPrefix(path, extPrefix) {
		at = from
		if at == "" || strings.HasPrefix(at, extPrefix) {
			at = "."
		}
	}
	root := pack.ProjectRoot(rp.Repo.Path, at)
	chosen, command, ok := pack.ResolveIn(root, packs.Look(root, rp.Repo.Path))
	sv.chosen, sv.root = chosen, root
	if !ok {
		first := pack.Servers[0]
		return sv, fmt.Errorf("%w (%s): install %s with: %s", lsp.ErrNoServer, language, first.Command, first.Install)
	}
	args := make([]string, len(chosen.Args))
	for i, a := range chosen.Args {
		args[i] = strings.ReplaceAll(a, "{data}", filepath.Join(s.Data, "lsp", chosen.Command, domain.Hash([]byte(root))[:12]))
	}
	if chosen.Slow {
		_ = os.MkdirAll(filepath.Join(s.Data, "lsp", chosen.Command, domain.Hash([]byte(root))[:12]), 0o700)
	}
	if s.lspRuns.latest(root, chosen.Command).State == "" {
		s.lspRuns.note(root, chosen.Command, "starting", "")
	}
	timeout := 60 * time.Second
	if chosen.Slow {
		timeout = 3 * time.Minute
	}
	env := lsp.Env("PATH=" + packs.SearchPath(command, root, rp.Repo.Path))
	if venv := filepath.Join(root, ".venv"); dirExists(venv) {
		env = append(env, "VIRTUAL_ENV="+venv)
	}
	running, err := s.lsps.Get(ctx, lsp.Options{Root: root, Command: command, Args: args, Env: env, InitTimeout: timeout, InitOptions: initOptions(root, chosen.Command, command)})
	if err != nil {
		s.lspRuns.note(root, chosen.Command, "failed", err.Error())
		return sv, err
	}
	sv.running, sv.uri = running, lsp.URI(abs)
	if err = running.Sync(sv.uri, packs.LanguageID(path, language), file.Content); err != nil {
		s.lspRuns.note(root, chosen.Command, "failed", err.Error())
		return sv, err
	}
	return sv, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func (s *Server) lsp(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var input struct {
		Path         string `json:"path"`
		From         string `json:"from"` // for a file outside the folder: the folder file it was reached from
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
	sv, err := s.server(ctx, rp, input.Path, input.From)
	if err != nil {
		fail(w, err)
		return
	}
	if sv.file.Hash != input.ExpectedHash && input.ExpectedHash != "" {
		fail(w, errors.New("document changed; reload before requesting semantic information"))
		return
	}
	client := sv.running.Client()
	// Who calls this, where is it implemented: only a server that has read the project knows. Wait for it
	// to finish (a while; a big Java or Rust project can take minutes, and then the answer says so).
	partial := false
	if needsIndex {
		limit := 20 * time.Second
		if sv.chosen.Slow {
			limit = 90 * time.Second
		}
		partial = !client.WaitIdle(ctx, limit)
	}
	params := map[string]any{"textDocument": map[string]string{"uri": sv.uri}}
	if input.Method != "textDocument/documentSymbol" {
		// The editor counts characters; the LSP counts UTF-16 units.
		params["position"] = map[string]int{"line": input.Line, "character": utf16Column(sv.file.Content, input.Line, input.Character)}
	}
	if input.Method == "textDocument/references" {
		params["context"] = map[string]bool{"includeDeclaration": true}
	}
	result, err := client.Call(ctx, input.Method, params)
	if err != nil {
		// A server that does not implement the request is working; it just cannot answer this one.
		if strings.Contains(err.Error(), "-32601") {
			s.lspRuns.note(sv.root, sv.chosen.Command, "ready", "does not answer "+strings.TrimPrefix(input.Method, "textDocument/")+" requests")
		} else {
			s.lspRuns.note(sv.root, sv.chosen.Command, "failed", err.Error())
		}
		fail(w, err)
		return
	}
	s.lspRuns.note(sv.root, sv.chosen.Command, "ready", "")
	current, _, err := s.readContent(rp, input.Path)
	if err != nil || current.Hash != sv.file.Hash {
		fail(w, errors.New("stale LSP response rejected after source edit"))
		return
	}
	out := map[string]any{"result": result, "hash": sv.file.Hash, "server": sv.chosen.Command, "partial": partial, "diagnostics": client.Diagnostics(sv.uri)}
	switch input.Method {
	case "textDocument/hover":
		out["hover"] = hoverText(result)
	case "textDocument/documentSymbol":
	default:
		out["locations"] = s.locations(rp, result)
	}
	respond(w, out)
}

// utf16Column converts a character column of a line to UTF-16 units.
func utf16Column(content string, line, chars int) int {
	for i := 0; i < line; i++ {
		j := strings.IndexByte(content, '\n')
		if j < 0 {
			return chars
		}
		content = content[j+1:]
	}
	if j := strings.IndexByte(content, '\n'); j >= 0 {
		content = content[:j]
	}
	units, n := 0, 0
	for _, r := range content {
		if n >= chars {
			break
		}
		units += len(utf16.Encode([]rune{r}))
		n++
	}
	return units + max(0, chars-n)
}

// lspDiagnostics gives the problems a language server reports for a file: errors, warnings, hints.
func (s *Server) lspDiagnostics(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var input struct {
		Path string `json:"path"`
		From string `json:"from"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	before := time.Now()
	sv, err := s.server(ctx, rp, input.Path, input.From)
	if err != nil {
		fail(w, err)
		return
	}
	client := sv.running.Client()
	limit := 4 * time.Second
	if sv.chosen.Slow {
		limit = 15 * time.Second
	}
	var raw json.RawMessage
	if client.PullsDiagnostics() {
		raw, err = client.PullDiagnostics(ctx, sv.uri)
	}
	if raw == nil || err != nil {
		raw = client.WaitDiagnostics(ctx, sv.uri, before, limit)
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
	lines := strings.Split(sv.file.Content, "\n")
	col := func(line, units int) int {
		if line < len(lines) {
			return characters(strings.TrimSuffix(lines[line], "\r"), units)
		}
		return units
	}
	out := []map[string]any{}
	for _, d := range published.Diagnostics {
		sev := names[d.Severity]
		if sev == "" {
			sev = "error"
		}
		out = append(out, map[string]any{"line": d.Range.Start.Line + 1, "character": col(d.Range.Start.Line, d.Range.Start.Character), "end_line": d.Range.End.Line + 1, "end_character": col(d.Range.End.Line, d.Range.End.Character), "severity": sev, "message": d.Message, "source": d.Source, "code": d.Code})
	}
	busy, doing := client.Busy()
	respond(w, map[string]any{"path": input.Path, "hash": sv.file.Hash, "server": sv.chosen.Command, "diagnostics": out, "indexing": busy, "doing": doing})
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
