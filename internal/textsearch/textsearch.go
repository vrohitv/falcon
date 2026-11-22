// Package textsearch finds text in a folder's files, in-process: the files are read on every core and
// matched line by line, as grep does (each matching line once, with the column of its first match).
// A plain search is a byte search (lowercased ASCII for a search that ignores case); a regular
// expression first tests the whole file, then the lines of the files it matches.
package textsearch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
)

// Options say what to find.
type Options struct {
	Query   string
	Regex   bool // a regular expression (RE2 syntax), else a literal
	Case    bool // match case
	Word    bool // whole words only
	PerFile int  // at most this many lines per file (0: 50)
}

// Hit is one matching line.
type Hit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Column  int    `json:"column"` // 1-based, in bytes
	Content string `json:"content"`
}

const maxFile = 64 << 20 // larger files are left out

// Matcher finds matches in one file's bytes.
type Matcher struct {
	o      Options
	lit    []byte // the literal (lowercased when case is ignored and it is ASCII)
	fold   bool   // compare lowercased ASCII
	re     *regexp.Regexp
	reFile *regexp.Regexp // the same, multi-line, to test a whole file at once
}

// Compile prepares a search.
func Compile(o Options) (*Matcher, error) {
	if o.PerFile <= 0 {
		o.PerFile = 50
	}
	m := &Matcher{o: o}
	ascii := true
	for i := 0; i < len(o.Query); i++ {
		if o.Query[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if !o.Regex && (o.Case || ascii) {
		m.lit = []byte(o.Query)
		if !o.Case {
			m.fold = true
			m.lit = lowerASCII(nil, m.lit)
		}
		return m, nil
	}
	expr := o.Query
	if !o.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	flags := ""
	if !o.Case {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + expr)
	if err != nil {
		return nil, err
	}
	reFile, err := regexp.Compile("(?m)" + flags + expr)
	if err != nil {
		return nil, err
	}
	m.re, m.reFile = re, reFile
	return m, nil
}

func lowerASCII(dst, b []byte) []byte {
	dst = append(dst[:0], b...)
	for i, c := range dst {
		if 'A' <= c && c <= 'Z' {
			dst[i] = c + 'a' - 'A'
		}
	}
	return dst
}

func isWord(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= utf8.RuneSelf
}

// wordAt: the match [s, e) of line stands as a whole word.
func wordAt(line []byte, s, e int) bool {
	return (s == 0 || !isWord(line[s-1])) && (e == len(line) || !isWord(line[e]))
}

// first is the first match in a line (start, ok), honouring whole words.
func (m *Matcher) first(line, folded []byte) (int, bool) {
	from := 0
	for from <= len(line) {
		var s, e int
		if m.re != nil {
			loc := m.re.FindIndex(line[from:])
			if loc == nil {
				return 0, false
			}
			s, e = from+loc[0], from+loc[1]
		} else {
			hay := line
			if m.fold {
				hay = folded
			}
			i := bytes.Index(hay[from:], m.lit)
			if i < 0 {
				return 0, false
			}
			s, e = from+i, from+i+len(m.lit)
		}
		if !m.o.Word || wordAt(line, s, e) {
			return s, true
		}
		from = s + 1
	}
	return 0, false
}

// Match reports the matching lines of one file's content (nil when none, or it is binary).
func (m *Matcher) Match(path string, b []byte, scratch *[]byte) []Hit {
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return nil // binary, as grep -I
	}
	// The whole file first: most files a search reads do not match.
	var folded []byte
	if m.re != nil {
		// (Lines ending in CR LF are matched without the CR, as in an editor; the whole-file test would
		// see the CR, so it is skipped for them.)
		if bytes.IndexByte(b, '\r') < 0 && !m.reFile.Match(b) {
			return nil
		}
	} else {
		hay := b
		if m.fold {
			*scratch = lowerASCII(*scratch, b)
			folded = *scratch
			hay = folded
		}
		if !bytes.Contains(hay, m.lit) {
			return nil
		}
	}
	var hits []Hit
	line := 1
	for start := 0; start <= len(b); {
		end := bytes.IndexByte(b[start:], '\n')
		if end < 0 {
			end = len(b)
		} else {
			end += start
		}
		lineEnd := end
		if lineEnd > start && b[lineEnd-1] == '\r' {
			lineEnd--
		}
		text := b[start:lineEnd]
		var ftext []byte
		if folded != nil {
			ftext = folded[start:lineEnd]
		}
		if col, ok := m.first(text, ftext); ok {
			content := text
			if len(content) > 400 {
				content = content[:400]
			}
			hits = append(hits, Hit{Path: path, Line: line, Column: col + 1, Content: strings.ToValidUTF8(string(content), "")})
			if len(hits) >= m.o.PerFile {
				break
			}
		}
		if end == len(b) {
			break
		}
		start = end + 1
		line++
	}
	return hits
}

// Search reads the files (folder-relative paths under root) on every core and returns their matching
// lines, in the order of files, and every file that matched.
func Search(ctx context.Context, root string, files []string, m *Matcher) (hits []Hit, matched []string, err error) {
	results := make([][]Hit, len(files))
	var next int
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := max(4, runtime.NumCPU())
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var scratch []byte
			for {
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(files) {
					return
				}
				abs := filepath.Join(root, filepath.FromSlash(files[i]))
				info, err := os.Stat(abs)
				if err != nil || !info.Mode().IsRegular() || info.Size() > maxFile {
					continue
				}
				b, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				results[i] = m.Match(files[i], b, &scratch)
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	for i, r := range results {
		if len(r) > 0 {
			matched = append(matched, files[i])
			hits = append(hits, r...)
		}
	}
	return hits, matched, nil
}

// RequiredLiterals are the plain strings (three bytes or more) that any match of a regular expression
// must contain: the literal pieces of its top-level sequence. A file without one of them cannot match,
// so an index of the files' text can narrow a regular expression search too. None when the expression
// has no such piece (an alternation at the top, or only classes and repeats).
func RequiredLiterals(expr string, matchCase bool) []string {
	flags := syntax.Perl
	if !matchCase {
		flags |= syntax.FoldCase
	}
	re, err := syntax.Parse(expr, flags)
	if err != nil {
		return nil
	}
	re = re.Simplify()
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	var parts []*syntax.Regexp
	switch re.Op {
	case syntax.OpLiteral:
		parts = []*syntax.Regexp{re}
	case syntax.OpConcat:
		parts = re.Sub
	}
	var out []string
	for _, p := range parts {
		for p.Op == syntax.OpCapture && len(p.Sub) == 1 {
			p = p.Sub[0]
		}
		if p.Op == syntax.OpLiteral && len(string(p.Rune)) >= 3 {
			out = append(out, string(p.Rune))
		}
	}
	return out
}
