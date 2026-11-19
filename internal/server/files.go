package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"github.com/vrohitv/falcon/internal/sysenv"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vrohitv/falcon/internal/ide"
	"github.com/vrohitv/falcon/internal/outline"
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

// symbols searches every declared name in the folder (go to symbol), from the symbol index.
func (s *Server) symbols(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	x := s.index(rp)
	x.Warm(3 * time.Second)
	respond(w, map[string]any{"ready": x.Ready(), "symbols": x.Search(r.URL.Query().Get("q"), 100)})
}

// codeWindow is a window of a file's lines, highlighted, for the viewer (which only asks for what is on
// screen, so a 100,000-line file opens as fast as a short one).
func (s *Server) codeWindow(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	file := r.URL.Query().Get("path")
	abs, err := rp.Repo.ReadPath(file)
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
	f, err := rp.Repo.Read(r.URL.Query().Get("path"))
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
	s.index(rp).Invalidate()
	respond(w, f)
}

type hit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Content string `json:"content"`
}

// search is git grep over tracked and untracked (not ignored) files: fast, literal by default, case-
// insensitive unless Match case is on (as in VS Code: no guessing from capitals), or a regular
// expression when asked.
func (s *Server) search(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" || len(q) > 500 {
		respond(w, []hit{})
		return
	}
	limit := 500
	args := []string{"-c", "core.quotePath=false", "grep", "-n", "--column", "-I", "--untracked", "--no-color", "--full-name", "-z", "--max-count=50"}
	if r.URL.Query().Get("regex") == "true" {
		args = append(args, "-E")
	} else {
		args = append(args, "-F")
	}
	if r.URL.Query().Get("case") != "true" {
		args = append(args, "-i")
	}
	if r.URL.Query().Get("word") == "true" {
		args = append(args, "-w")
	}
	args = append(args, "-e", q, "--")
	if glob := strings.TrimSpace(r.URL.Query().Get("glob")); glob != "" {
		for _, g := range strings.Split(glob, ",") {
			g = strings.TrimSpace(g)
			if g == "" || strings.HasPrefix(g, "-") {
				continue
			}
			// As in VS Code: a pattern without a slash matches at any depth; a folder means everything in it.
			if !strings.Contains(g, "/") {
				g = "**/" + g
			} else if strings.HasSuffix(g, "/") {
				g += "**"
			}
			args = append(args, ":(glob)"+g)
		}
	}
	// The folder's text index proposes the only files that can match a literal search; git grep then
	// reads just those. (Regular expressions search everything.)
	x := s.index(rp)
	indexed := false
	if r.URL.Query().Get("regex") != "true" {
		if cands, ok := x.Candidates(r.Context(), q); ok {
			if globs := globPatterns(r.URL.Query().Get("glob")); len(globs) > 0 {
				kept := cands[:0]
				for _, c := range cands {
					for _, g := range globs {
						if g.MatchString(c) {
							kept = append(kept, c)
							break
						}
					}
				}
				cands = kept
			}
			if len(cands) == 0 {
				respond(w, []hit{})
				return
			}
			if len(cands) <= 5000 {
				for i, a := range args {
					if a == "--" {
						args = args[:i+1]
						for _, f := range cands {
							args = append(args, ":(literal)"+f)
						}
						break
					}
				}
				indexed = true
			}
		}
	}
	// Typing narrows a search: when the new text contains the previous one (same options, nothing changed
	// since), only the files that matched before can match now, and git grep reads just those. Never for
	// regular expressions or whole words, where containing the old text does not mean matching it.
	insensitive := r.URL.Query().Get("case") != "true"
	opts := r.URL.Query().Get("case") + "|" + r.URL.Query().Get("word") + "|" + r.URL.Query().Get("regex") + "|" + r.URL.Query().Get("glob")
	literal := r.URL.Query().Get("regex") != "true" && r.URL.Query().Get("word") != "true"
	if prev, ok := s.lastSearch(rp.ID); ok && literal && !indexed && prev.opts == opts && prev.gen == x.Gen() && time.Since(prev.at) < 10*time.Second &&
		len(q) > len(prev.q) && len(prev.files) <= 5000 &&
		(strings.Contains(q, prev.q) || (prev.insensitive && strings.Contains(strings.ToLower(q), strings.ToLower(prev.q)))) {
		if len(prev.files) == 0 {
			s.rememberSearch(rp.ID, search{q: q, opts: opts, insensitive: insensitive, gen: x.Gen(), at: time.Now(), files: []string{}})
			respond(w, []hit{})
			return
		}
		for i, a := range args {
			if a == "--" {
				args = args[:i+1]
				for _, f := range prev.files {
					args = append(args, ":(literal)"+f)
				}
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rp.Repo.Path
	cmd.Env = sysenv.Git()
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			s.rememberSearch(rp.ID, search{q: q, opts: opts, insensitive: insensitive, gen: x.Gen(), at: time.Now(), files: []string{}})
			respond(w, []hit{})
			return
		}
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			fail(w, errors.New(strings.TrimSpace(string(exit.Stderr))))
			return
		}
		fail(w, err)
		return
	}
	hits := []hit{}
	// Every matching file is noted (not just those in the first 500 hits), for narrowing the next search.
	seen := map[string]bool{}
	files := []string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		// path NUL line NUL column NUL text
		p := bytes.SplitN(sc.Bytes(), []byte{0}, 4)
		if len(p) != 4 {
			continue
		}
		if name := string(p[0]); !seen[name] {
			seen[name] = true
			files = append(files, name)
		}
		if len(hits) >= limit {
			continue
		}
		line, _ := strconv.Atoi(string(p[1]))
		col, _ := strconv.Atoi(string(p[2]))
		text := string(p[3])
		if len(text) > 400 {
			text = text[:400]
		}
		hits = append(hits, hit{Path: string(p[0]), Line: line, Column: col, Content: text})
	}
	s.rememberSearch(rp.ID, search{q: q, opts: opts, insensitive: insensitive, gen: x.Gen(), at: time.Now(), files: files})
	respond(w, hits)
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
