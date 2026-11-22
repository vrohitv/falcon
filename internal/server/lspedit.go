package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/lsp"
	"github.com/vrohitv/falcon/internal/workspace"
)

// Edits a language server proposes (a rename, a quick fix, a command's result) are applied by Falcon,
// never by the server: each file is read, the edits are made in it, and it is written only if it is
// still the version that was read (a file changed meanwhile fails the whole edit, and nothing is
// written). Only files inside the folder are touched; deleting files is refused.

type lspPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type lspRange struct {
	Start lspPos `json:"start"`
	End   lspPos `json:"end"`
}
type textEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

// lineStart is the byte offset where a line (0-based) begins, or len(content) past the end.
func lineStart(content string, line int) int {
	off := 0
	for i := 0; i < line; i++ {
		j := strings.IndexByte(content[off:], '\n')
		if j < 0 {
			return len(content)
		}
		off += j + 1
	}
	return off
}

// byteOffset is a position (a line and UTF-16 units into it) as a byte offset into content.
func byteOffset(content string, p lspPos) int {
	off := lineStart(content, p.Line)
	end := len(content)
	if j := strings.IndexByte(content[off:], '\n'); j >= 0 {
		end = off + j
	}
	units := 0
	for i, r := range content[off:end] {
		if units >= p.Character {
			return off + i
		}
		units += utf16.RuneLen(r)
		if r == utf8.RuneError {
			units = max(units, 1)
		}
	}
	return end
}

// charColumn is a position's column in characters (the editor's unit) rather than UTF-16 units.
func charColumn(content string, p lspPos) int {
	off := lineStart(content, p.Line)
	end := len(content)
	if j := strings.IndexByte(content[off:], '\n'); j >= 0 {
		end = off + j
	}
	units, chars := 0, 0
	for _, r := range content[off:end] {
		if units >= p.Character {
			break
		}
		units += utf16.RuneLen(r)
		chars++
	}
	return chars
}

// fileEdits are the text edits for one folder file.
type fileEdits struct {
	Path  string
	Edits []textEdit
}

// editPlan is a workspace edit checked and resolved to folder paths.
type editPlan struct {
	Files   []fileEdits
	Creates []string
	Renames [][2]string
}

// folderPath is a file: URI as a path inside the folder ("" when it is outside).
func folderPath(rp *workspace.Repo, uri string) string {
	abs := lsp.FromURI(uri)
	if abs == "" {
		return ""
	}
	for _, root := range []string{rp.Repo.Path, evalOr(rp.Repo.Path)} {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

func evalOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// planEdit reads a WorkspaceEdit (changes, or documentChanges with text edits, creates and renames).
func planEdit(rp *workspace.Repo, raw json.RawMessage) (editPlan, error) {
	var we struct {
		Changes         map[string][]textEdit `json:"changes"`
		DocumentChanges []json.RawMessage     `json:"documentChanges"`
	}
	var plan editPlan
	if err := json.Unmarshal(raw, &we); err != nil {
		return plan, fmt.Errorf("the language server's edit could not be read: %w", err)
	}
	outside := errors.New("the edit changes files outside the folder; nothing was changed")
	byPath := map[string]int{}
	add := func(p string, edits []textEdit) {
		if i, ok := byPath[p]; ok {
			plan.Files[i].Edits = append(plan.Files[i].Edits, edits...)
			return
		}
		byPath[p] = len(plan.Files)
		plan.Files = append(plan.Files, fileEdits{Path: p, Edits: edits})
	}
	uris := make([]string, 0, len(we.Changes))
	for uri := range we.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		p := folderPath(rp, uri)
		if p == "" {
			return plan, outside
		}
		add(p, we.Changes[uri])
	}
	for _, dc := range we.DocumentChanges {
		var op struct {
			Kind         string `json:"kind"`
			URI          string `json:"uri"`
			OldURI       string `json:"oldUri"`
			NewURI       string `json:"newUri"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []textEdit `json:"edits"`
		}
		if err := json.Unmarshal(dc, &op); err != nil {
			return plan, err
		}
		switch op.Kind {
		case "":
			p := folderPath(rp, op.TextDocument.URI)
			if p == "" {
				return plan, outside
			}
			add(p, op.Edits)
		case "create":
			p := folderPath(rp, op.URI)
			if p == "" {
				return plan, outside
			}
			plan.Creates = append(plan.Creates, p)
		case "rename":
			from, to := folderPath(rp, op.OldURI), folderPath(rp, op.NewURI)
			if from == "" || to == "" {
				return plan, outside
			}
			plan.Renames = append(plan.Renames, [2]string{from, to})
		default:
			return plan, errors.New("the edit deletes files; Falcon does not let a language server delete files")
		}
	}
	return plan, nil
}

// applyText makes edits (positions in the original text) in content.
func applyText(content string, edits []textEdit) (string, error) {
	type span struct {
		from, to int
		text     string
	}
	spans := make([]span, len(edits))
	for i, e := range edits {
		spans[i] = span{byteOffset(content, e.Range.Start), byteOffset(content, e.Range.End), e.NewText}
		if spans[i].to < spans[i].from {
			return "", errors.New("the language server sent an edit whose end is before its start")
		}
	}
	// Last first, so earlier offsets stay right. Edits at the same place keep their order.
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].from > spans[j].from })
	for i := 1; i < len(spans); i++ {
		if spans[i].to > spans[i-1].from {
			return "", errors.New("the language server sent overlapping edits")
		}
	}
	for _, s := range spans {
		content = content[:s.from] + s.text + content[s.to:]
	}
	return content, nil
}

// editPreview is what an edit does to one file, for the owner to see before it is applied.
type editPreview struct {
	Path    string        `json:"path"`
	Changes []editLineRow `json:"changes"`
	Created bool          `json:"created,omitempty"`
	Renamed string        `json:"renamed,omitempty"` // the new path
}
type editLineRow struct {
	Line   int    `json:"line"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func preview(rp *workspace.Repo, plan editPlan) ([]editPreview, error) {
	out := []editPreview{}
	for _, f := range plan.Files {
		cur, err := rp.Repo.Read(f.Path)
		if err != nil {
			return nil, err
		}
		next, err := applyText(cur.Content, f.Edits)
		if err != nil {
			return nil, err
		}
		before, after := strings.Split(cur.Content, "\n"), strings.Split(next, "\n")
		lines := map[int]bool{}
		for _, e := range f.Edits {
			lines[e.Range.Start.Line] = true
		}
		rows := []editLineRow{}
		shift := 0 // lines added before this point
		sorted := make([]int, 0, len(lines))
		for l := range lines {
			sorted = append(sorted, l)
		}
		sort.Ints(sorted)
		for _, l := range sorted {
			row := editLineRow{Line: l + 1}
			if l < len(before) {
				row.Before = clip(before[l])
			}
			if l+shift < len(after) && l+shift >= 0 {
				row.After = clip(after[l+shift])
			}
			for _, e := range f.Edits {
				if e.Range.Start.Line == l {
					shift += strings.Count(e.NewText, "\n") - (e.Range.End.Line - e.Range.Start.Line)
				}
			}
			rows = append(rows, row)
		}
		out = append(out, editPreview{Path: f.Path, Changes: rows})
	}
	for _, p := range plan.Creates {
		out = append(out, editPreview{Path: p, Created: true})
	}
	for _, r := range plan.Renames {
		out = append(out, editPreview{Path: r[0], Renamed: r[1]})
	}
	return out, nil
}

func clip(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// apply makes the edit: every file is read and edited first; then all are written, each only if it is
// still the version read. It returns the paths changed.
func (s *Server) apply(rp *workspace.Repo, plan editPlan) ([]string, error) {
	type write struct{ path, content, hash string }
	writes := []write{}
	for _, f := range plan.Files {
		cur, err := rp.Repo.Read(f.Path)
		if err != nil {
			return nil, err
		}
		next, err := applyText(cur.Content, f.Edits)
		if err != nil {
			return nil, err
		}
		if next != cur.Content {
			writes = append(writes, write{f.Path, next, cur.Hash})
		}
	}
	changed := []string{}
	for _, w := range writes {
		if _, err := rp.Repo.Write(w.path, w.content, w.hash); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				err = fmt.Errorf("%s changed while the edit was being made; edits already written: %v", w.path, changed)
			}
			s.index(rp).Changed(changed...)
			return changed, err
		}
		changed = append(changed, w.path)
	}
	for _, p := range plan.Creates {
		if _, err := rp.Repo.ReadPath(p); err != nil {
			return changed, err
		}
		if _, err := os.Stat(filepath.Join(rp.Repo.Path, filepath.FromSlash(p))); os.IsNotExist(err) {
			if _, err := rp.Repo.Write(p, "", ""); err != nil {
				return changed, err
			}
			changed = append(changed, p)
		}
	}
	for _, r := range plan.Renames {
		from, err := rp.Repo.ReadPath(r[0])
		if err != nil {
			return changed, err
		}
		to, err := rp.Repo.ReadPath(r[1])
		if err != nil {
			return changed, err
		}
		if _, err := os.Stat(to); err == nil {
			return changed, fmt.Errorf("%s already exists", r[1])
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return changed, err
		}
		if err := os.Rename(from, to); err != nil {
			return changed, err
		}
		changed = append(changed, r[0], r[1])
	}
	s.index(rp).Changed(changed...)
	return changed, nil
}

// pendingEdits keep a proposed edit (or code action) between showing it and applying it.
type pendingEdits struct {
	mu    sync.Mutex
	items map[string]pendingEdit
}

type pendingEdit struct {
	repo    string
	plan    *editPlan
	action  json.RawMessage // a code action still to resolve or run
	running *lsp.Running
	at      time.Time
}

func (p *pendingEdits) put(e pendingEdit) string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.items == nil {
		p.items = map[string]pendingEdit{}
	}
	for k, v := range p.items {
		if time.Since(v.at) > 10*time.Minute {
			delete(p.items, k)
		}
	}
	e.at = time.Now()
	p.items[token] = e
	return token
}

func (p *pendingEdits) take(repo, token string) (pendingEdit, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[token]
	if !ok || e.repo != repo {
		return pendingEdit{}, false
	}
	delete(p.items, token)
	return e, true
}
