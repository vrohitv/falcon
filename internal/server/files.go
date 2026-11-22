package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/ide"
	"github.com/vrohitv/falcon/internal/outline"
	"github.com/vrohitv/falcon/internal/textsearch"
	"github.com/vrohitv/falcon/internal/workspace"
)

func (s *Server) directory(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	entries, err := rp.Repo.Directory(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, entries)
}

func (s *Server) files(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	files, err := s.index(rp).Files(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, files)
}

func (s *Server) fuzzy(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	res, err := s.index(rp).Fuzzy(r.Context(), r.URL.Query().Get("q"), 80)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, res)
}

// symbols searches every declared name in the folder (go to symbol): the folder's own symbol index, and
// the language servers running for it (workspace/symbol), which know what the index's patterns cannot.
func (s *Server) symbols(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	x := s.index(rp)
	x.Warm(3 * time.Second)
	q := r.URL.Query().Get("q")
	type sym struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Container string `json:"container,omitempty"`
	}
	out := []sym{}
	seen := map[string]bool{}
	for _, h := range x.Search(q, 100) {
		seen[h.Path+":"+strconv.Itoa(h.Line)] = true
		out = append(out, sym{Name: h.Name, Kind: h.Kind, Path: h.Path, Line: h.Line})
	}
	if strings.TrimSpace(q) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
		defer cancel()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range s.lsps.Clients(rp.Repo.Path) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				raw, err := c.Call(ctx, "workspace/symbol", map[string]any{"query": q})
				if err != nil {
					return
				}
				found := s.lspSymbols(rp, raw)
				mu.Lock()
				defer mu.Unlock()
				for _, f := range found {
					p, _ := f["path"].(string)
					line, _ := f["line"].(int)
					if seen[p+":"+strconv.Itoa(line)] {
						continue
					}
					seen[p+":"+strconv.Itoa(line)] = true
					out = append(out, sym{Name: f["name"].(string), Kind: symbolKind(f["kind"].(int)), Path: p, Line: line, Container: f["container"].(string)})
				}
			}()
		}
		wg.Wait()
	}
	respond(w, map[string]any{"ready": x.Ready(), "symbols": out})
}

// symbolKind names an LSP SymbolKind the way the index names its kinds.
func symbolKind(k int) string {
	names := map[int]string{1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class", 6: "method", 7: "property", 8: "field",
		9: "constructor", 10: "enum", 11: "interface", 12: "function", 13: "variable", 14: "constant", 22: "member", 23: "struct", 26: "type"}
	if n, ok := names[k]; ok {
		return n
	}
	return "symbol"
}

// codeWindow is a window of a file's lines, highlighted, for the viewer (which only asks for what is on
// screen, so a 100,000-line file opens as fast as a short one).
func (s *Server) codeWindow(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	file := r.URL.Query().Get("path")
	abs, err := s.readable(rp, file)
	if err != nil {
		fail(w, err)
		return
	}
	doc, err := ide.Open(abs, file)
	if err != nil {
		fail(w, err)
		return
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count <= 0 || count > 1000 {
		count = 200
	}
	start = max(0, min(start, doc.Total))
	if hash := r.URL.Query().Get("hash"); hash != "" && hash != doc.Hash {
		fail(w, errors.New("source changed; reload the document"))
		return
	}
	lines, exact := doc.Lines(start, min(start+count, doc.Total))
	_, refine := doc.Exact()
	raw := []string{}
	if r.URL.Query().Get("raw") == "true" {
		for i := start; i < min(start+count, doc.Total); i++ {
			raw = append(raw, doc.Raw(i+1))
		}
	}
	respond(w, map[string]any{"path": file, "start": start, "total": doc.Total, "max_columns": doc.MaxCols, "language": doc.Lang, "hash": doc.Hash, "lines": lines, "exact": exact, "refine": refine && !exact, "raw": raw})
}

// codeShape is a file's outline for the minimap: two bytes a line (indent, length), base64.
func (s *Server) codeShape(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	file := r.URL.Query().Get("path")
	abs, err := s.readable(rp, file)
	if err != nil {
		fail(w, err)
		return
	}
	doc, err := ide.Open(abs, file)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, map[string]any{"hash": doc.Hash, "total": doc.Total, "shape": base64.StdEncoding.EncodeToString(doc.Shape())})
}

// highlight colours an editor's unsaved text; the editor sends only the lines around what is on screen.
func (s *Server) highlight(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var req struct {
		Path string `json:"path"`
		Text string `json:"text"`
	}
	if err := body(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	if len(req.Text) > 1<<20 {
		fail(w, errors.New("text too large to highlight"))
		return
	}
	respond(w, map[string]any{"lines": ide.HighlightText(req.Path, req.Text)})
}

func (s *Server) outline(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	f, _, err := s.readContent(rp, r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, outline.Of(f.Path, f.Content))
}

func (s *Server) file(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	if commit := r.URL.Query().Get("commit"); commit != "" {
		content, err := rp.Repo.Show(r.Context(), commit, r.URL.Query().Get("path"))
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, map[string]string{"content": content})
		return
	}
	content, err := rp.Repo.Read(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, content)
}

// saveFile writes a file only if it still is what the editor loaded (expected_hash), so an edit made
// elsewhere in the meantime is never overwritten.
func (s *Server) saveFile(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var input struct {
		Path         string `json:"path"`
		Content      string `json:"content"`
		ExpectedHash string `json:"expected_hash"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	f, err := rp.Repo.Write(input.Path, input.Content, input.ExpectedHash)
	if err != nil {
		fail(w, err)
		return
	}
	s.index(rp).Changed(input.Path) // in the index before the answer, not when the watcher reports it
	respond(w, f)
}

// search finds text in tracked and untracked (not ignored) files, in-process: literal by default,
// case-insensitive unless Match case is on (as in VS Code: no guessing from capitals), or a regular
// expression when asked. The folder's text index proposes the only files a literal can be in.
func (s *Server) search(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	qs := r.URL.Query()
	q := qs.Get("q")
	if strings.TrimSpace(q) == "" || len(q) > 500 {
		respond(w, []textsearch.Hit{})
		return
	}
	const limit = 500
	o := textsearch.Options{Query: q, Regex: qs.Get("regex") == "true", Case: qs.Get("case") == "true", Word: qs.Get("word") == "true"}
	m, err := textsearch.Compile(o)
	if err != nil {
		fail(w, err)
		return
	}
	x := s.index(rp)
	var files []string
	indexed := false
	if !o.Regex {
		if cands, ok := x.Candidates(r.Context(), q); ok {
			files, indexed = cands, true
		}
	} else {
		// A regular expression's literal pieces must all be in a matching file.
		for _, lit := range textsearch.RequiredLiterals(q, o.Case) {
			cands, ok := x.Candidates(r.Context(), lit)
			if !ok {
				continue
			}
			if !indexed {
				files, indexed = cands, true
				continue
			}
			files = intersectSorted(files, cands)
		}
	}
	// Typing narrows a search: when the new text contains the previous one (same options, nothing changed
	// since), only the files that matched before can match now. Never for regular expressions or whole
	// words, where containing the old text does not mean matching it.
	opts := qs.Get("case") + "|" + qs.Get("word") + "|" + qs.Get("regex") + "|" + qs.Get("glob")
	literal := !o.Regex && !o.Word
	if prev, ok := s.lastSearch(rp.ID); !indexed && ok && literal && prev.opts == opts && prev.gen == x.Gen() && time.Since(prev.at) < 10*time.Second &&
		len(q) > len(prev.q) && (strings.Contains(q, prev.q) || (prev.insensitive && strings.Contains(strings.ToLower(q), strings.ToLower(prev.q)))) {
		files = prev.files
	}
	if files == nil {
		all, err := x.Files(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		files = make([]string, len(all))
		for i, f := range all {
			files[i] = f.Path
		}
	}
	if globs := globPatterns(qs.Get("glob")); len(globs) > 0 {
		kept := make([]string, 0, len(files))
		for _, f := range files {
			for _, g := range globs {
				if g.MatchString(f) {
					kept = append(kept, f)
					break
				}
			}
		}
		files = kept
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hits, matched, err := textsearch.Search(ctx, rp.Repo.Path, files, m)
	if err != nil {
		fail(w, err)
		return
	}
	if matched == nil {
		matched = []string{}
	}
	// Every matching file is noted (not just those in the first 500 hits), for narrowing the next search.
	s.rememberSearch(rp.ID, search{q: q, opts: opts, insensitive: !o.Case, gen: x.Gen(), at: time.Now(), files: matched})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if hits == nil {
		hits = []textsearch.Hit{}
	}
	respond(w, hits)
}

// intersectSorted keeps the strings in both sorted lists.
func intersectSorted(a, b []string) []string {
	out := make([]string, 0, min(len(a), len(b)))
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// summary is the folder at a glance: branch, head, how many changes.
func (s *Server) summary(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	// Opening a folder starts its index in the background (loaded from disk, then brought up to date).
	s.index(rp).Warm(10 * time.Second)
	s.index(rp).BuildText()
	st := rp.Repo.Status(r.Context())
	changes := 0
	if st.Changes != "" {
		changes = strings.Count(strings.TrimSuffix(st.Changes, "\n"), "\n") + 1
	}
	respond(w, map[string]any{"id": rp.ID, "name": rp.Name, "path": rp.Path, "branch": st.Branch, "head": st.Head, "changes": changes})
}

// search is the last search in a folder, for narrowing the next one.
type search struct {
	q, opts     string
	insensitive bool
	gen         uint64
	at          time.Time
	files       []string
}

func (s *Server) lastSearch(id string) (search, bool) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	v, ok := s.searches[id]
	return v, ok
}

func (s *Server) rememberSearch(id string, v search) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.searches == nil {
		s.searches = map[string]search{}
	}
	s.searches[id] = v
}

// globPatterns turns the search's file patterns into matchers with git's meaning (as VS Code does: a
// pattern without a slash matches at any depth, ** crosses folders, a trailing slash means a folder).
func globPatterns(spec string) []*regexp.Regexp {
	out := []*regexp.Regexp{}
	for _, g := range strings.Split(spec, ",") {
		g = strings.TrimSpace(g)
		if g == "" || strings.HasPrefix(g, "-") {
			continue
		}
		if !strings.Contains(g, "/") {
			g = "**/" + g
		} else if strings.HasSuffix(g, "/") {
			g += "**"
		}
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(g); i++ {
			switch c := g[i]; {
			case strings.HasPrefix(g[i:], "**/"):
				b.WriteString("(?:.*/)?")
				i += 2
			case strings.HasPrefix(g[i:], "**"):
				b.WriteString(".*")
				i++
			case c == '*':
				b.WriteString("[^/]*")
			case c == '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteString("$")
		if re, err := regexp.Compile(b.String()); err == nil {
			out = append(out, re)
		}
	}
	return out
}
