package server

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vrohitv/falcon/internal/textsearch"
	"github.com/vrohitv/falcon/internal/workspace"

	"github.com/vrohitv/falcon/internal/outline"
)

// definitions answers "go to definition" for a name under the pointer. It
// is syntactic, not semantic: Go files are parsed, other languages are matched
// on declaration shapes, and a component name finds its file. Matches in the
// file being read come first, then its folder, then the rest of the
// repository.
func (s *Server) definitions(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	name := r.URL.Query().Get("name")
	from := r.URL.Query().Get("from")
	qualifier := r.URL.Query().Get("qualifier")
	at, _ := strconv.Atoi(r.URL.Query().Get("line"))
	if !identifier.MatchString(name) {
		fail(w, errors.New("not an identifier"))
		return
	}
	x := s.index(rp)
	files, err := x.Files(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	decl := declaration(name)
	type hit struct {
		outline.Definition
		rank int
	}
	hits := []hit{}
	rankOf := func(p string) int {
		switch {
		case p == from:
			return 0
		case path.Dir(p) == path.Dir(from):
			return 1
		}
		return 2
	}
	// The symbol index answers from memory. Until it has been built, files that mention the name are read
	// (the text index narrows them down).
	indexed := x.Ready()
	x.Warm(3 * time.Second)
	if indexed {
		for _, sym := range x.Lookup(name) {
			hits = append(hits, hit{outline.Definition{Name: sym.Name, Kind: sym.Kind, Path: sym.Path, Line: sym.Line}, rankOf(sym.Path)})
		}
	}
	var mentions map[string]bool
	if !indexed {
		mentions = s.mentioning(r.Context(), rp, name)
	}
	for _, f := range files {
		rank := 2
		switch {
		case f.Path == from:
			rank = 0
		case path.Dir(f.Path) == path.Dir(from):
			rank = 1
		}
		base := path.Base(f.Path)
		if stem := strings.TrimSuffix(base, path.Ext(base)); stem == name && component[path.Ext(base)] {
			hits = append(hits, hit{outline.Definition{Name: name, Kind: "component", Path: f.Path, Line: 1}, rank})
			continue
		}
		if indexed || !code[path.Ext(base)] || (mentions != nil && !mentions[f.Path]) {
			continue
		}
		raw, err := rp.Repo.ReadText(f.Path)
		if err != nil || len(raw) > 1<<20 || !strings.Contains(string(raw), name) {
			continue
		}
		content := string(raw)
		if strings.HasSuffix(f.Path, ".go") {
			for _, d := range outline.Of(f.Path, content) {
				if d.Name == name || strings.HasSuffix(d.Name, "."+name) {
					hits = append(hits, hit{d, rank})
				}
			}
			continue
		}
		for _, loc := range decl.FindAllStringIndex(content, 20) {
			line := strings.Count(content[:loc[0]], "\n") + 1
			text := strings.TrimSpace(content[loc[0]:loc[1]])
			hits = append(hits, hit{outline.Definition{Name: name, Kind: "definition", Path: f.Path, Line: line, Signature: text}, rank})
		}
	}
	// pkg.Name: keep the matches in a folder called pkg, if there are any.
	if qualifier != "" {
		kept := hits[:0:0]
		for _, h := range hits {
			if path.Base(path.Dir(h.Path)) == qualifier {
				kept = append(kept, h)
			}
		}
		if len(kept) > 0 {
			hits = kept
		}
	}
	// Nothing declared at file level: a local variable or parameter, declared
	// on the nearest line above the use.
	if len(hits) == 0 && qualifier == "" && at > 0 {
		if raw, err := rp.Repo.ReadText(from); err == nil {
			rows := strings.Split(string(raw), "\n")
			local := localDeclaration(name)
			for i := min(at, len(rows)) - 1; i >= 0; i-- {
				if local.MatchString(rows[i]) {
					hits = append(hits, hit{outline.Definition{Name: name, Kind: "local", Path: from, Line: i + 1}, 0})
					break
				}
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := []outline.Definition{}
	for _, h := range hits {
		if len(out) == 20 {
			break
		}
		if h.Signature == "" {
			h.Signature = signature(rp, h.Path, h.Line)
		}
		out = append(out, h.Definition)
	}
	respond(w, out)
}

var (
	identifier = regexp.MustCompile(`^[A-Za-z_$][\w$]{0,127}$`)
	component  = map[string]bool{".svelte": true, ".vue": true, ".tsx": true, ".jsx": true}
	code       = map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".js": true, ".jsx": true,
		".mjs": true, ".cjs": true, ".svelte": true, ".vue": true, ".py": true, ".rs": true, ".rb": true,
		".java": true, ".kt": true, ".swift": true, ".c": true, ".h": true, ".cpp": true, ".cc": true,
		".hpp": true, ".cs": true, ".php": true, ".scala": true, ".dart": true, ".lua": true, ".zig": true,
	}
)

// declaration matches the line that declares name in most C-family and
// scripting languages: keyword declarations, and methods in a class body.
func declaration(name string) *regexp.Regexp {
	n := regexp.QuoteMeta(name)
	return regexp.MustCompile(`(?m)^[ \t]*(?:(?:export|default|declare|async|abstract|public|private|protected|static|readonly|override|pub(?:\([a-z]+\))?|unsafe|inline|final)\s+)*` +
		`(?:(?:function\*?|class|interface|type|enum|const|let|var|def|fn|struct|trait|mod|impl|module|object|func|fun|union)\s+` + n + `\b` +
		`|` + n + `\s*(?:<[^>]*>)?\s*\([^)\n]*\)\s*(?::[^{\n]*)?\{)[^\n]*`)
}

// localDeclaration matches a line that introduces name in a block: Go's :=
// and range, let/const/var, and parameter lists.
func localDeclaration(name string) *regexp.Regexp {
	n := regexp.QuoteMeta(name)
	return regexp.MustCompile(`(?:^|[^\w$.])` + n + `(?:\s*,\s*[\w$]+)*\s*:=` +
		`|\b(?:let|const|var|for)\b[^=\n]*?(?:^|[^\w$.])` + n + `\b` +
		`|\b(?:func|function|def|fn)\b[^\n]*\([^)]*\b` + n + `\b` +
		`|,\s*` + n + `(?:\s*,\s*[\w$]+)*\s*:=`)
}

func signature(rp *workspace.Repo, p string, line int) string {
	text, err := rp.Repo.ReadText(p)
	if err != nil {
		return ""
	}
	rows := strings.SplitN(string(text), "\n", line+1)
	if line < 1 || line > len(rows) {
		return ""
	}
	sig := strings.TrimSpace(rows[line-1])
	if len(sig) > 160 {
		sig = sig[:160] + "…"
	}
	return sig
}

// mentioning is the set of files containing name as a whole word, read in-process (the text index
// proposes the files that can contain it).
func (s *Server) mentioning(ctx context.Context, rp *workspace.Repo, name string) map[string]bool {
	x := s.index(rp)
	files, ok := x.Candidates(ctx, name)
	if !ok {
		all, err := x.Files(ctx)
		if err != nil {
			return nil
		}
		files = make([]string, len(all))
		for i, f := range all {
			files[i] = f.Path
		}
	}
	m, err := textsearch.Compile(textsearch.Options{Query: name, Case: true, Word: true, PerFile: 1})
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, matched, err := textsearch.Search(ctx, rp.Repo.Path, files, m)
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(matched))
	for _, p := range matched {
		set[p] = true
	}
	return set
}
