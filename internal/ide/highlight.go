package ide

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/vrohitv/falcon/internal/domain"
)

// Highlighting for the viewer, the editor and diffs: chroma's tokens become short CSS classes, one line
// of HTML per source line. Lexing a large file whole is too slow to wait for, so the viewer's windows are
// highlighted on their own, each with some lines before it (to enter strings and comments in the right
// state) and after it (so what opens in the window can close); small files also get one whole-file pass
// in the background, after which every window is exact.
const (
	maxFileBytes  = 64 << 20  // larger files are refused
	chunkLines    = 1000      // a window
	leadLines     = 300       // lexed before a window, then dropped
	tailLines     = 100       // lexed after a window, then dropped
	maxLexBytes   = 512 << 10 // a window larger than this is shown plain (a file of few, huge lines)
	widthCap      = 20000     // the widest line reported to the viewer
	fullPassBytes = 256 << 10 // files up to this size get a whole-file pass
	cacheBytes    = 32 << 20  // documents (source and rendered lines) kept in memory
)

// classFor gives a token its CSS class ("" for none). The names are kept short: they repeat once per
// token in every line sent.
func classFor(t chroma.TokenType) string {
	switch {
	case t == chroma.Error:
		// What a lexer could not read is not marked: that is the lexer's limit, not the code's.
		return ""
	case t == chroma.KeywordType:
		return "kt"
	case t == chroma.KeywordConstant:
		return "no"
	case t.InCategory(chroma.Keyword):
		return "k"
	case t == chroma.CommentPreproc || t == chroma.CommentPreprocFile:
		return "cp"
	case t.InCategory(chroma.Comment):
		return "c"
	case t.InCategory(chroma.LiteralString):
		return "s"
	case t.InCategory(chroma.LiteralNumber):
		return "m"
	case t.InCategory(chroma.Literal):
		return "s"
	case t == chroma.NameFunction || t == chroma.NameFunctionMagic:
		return "nf"
	case t == chroma.NameBuiltin || t == chroma.NameBuiltinPseudo:
		return "nb"
	case t == chroma.NameClass || t == chroma.NameNamespace || t == chroma.NameException:
		return "nc"
	case t == chroma.NameConstant:
		return "no"
	case t == chroma.NameDecorator:
		return "nd"
	case t == chroma.NameTag:
		return "nt"
	case t == chroma.NameAttribute:
		return "na"
	case t == chroma.NameProperty || t == chroma.NameLabel:
		return "np"
	case t == chroma.NameVariable || t == chroma.NameVariableInstance || t == chroma.NameVariableGlobal || t == chroma.NameVariableClass:
		return "nv"
	case t == chroma.GenericInserted:
		return "gi"
	case t == chroma.GenericDeleted:
		return "gd"
	case t == chroma.GenericHeading || t == chroma.GenericSubheading:
		return "gh"
	case t == chroma.GenericEmph:
		return "ge"
	case t == chroma.GenericStrong:
		return "gs"
	case t.InCategory(chroma.Generic):
		return "g"
	case t.InCategory(chroma.Operator):
		return "o"
	case t == chroma.Punctuation:
		return "p"
	}
	return ""
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// render lexes text into exactly n lines of HTML.
func render(lexer chroma.Lexer, text string, n int) []string {
	if lexer == nil {
		return plain(text, n)
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return plain(text, n)
	}
	lines := make([]string, 0, n)
	var line strings.Builder
	emit := func(class, part string) {
		if part == "" {
			return
		}
		if class == "" {
			line.WriteString(escaper.Replace(part))
			return
		}
		line.WriteString(`<i class="`)
		line.WriteString(class)
		line.WriteString(`">`)
		line.WriteString(escaper.Replace(part))
		line.WriteString(`</i>`)
	}
	for tok := it(); tok != chroma.EOF; tok = it() {
		class := classFor(tok.Type)
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				lines = append(lines, line.String())
				line.Reset()
			}
			emit(class, strings.TrimSuffix(part, "\r"))
		}
	}
	lines = append(lines, line.String())
	return fit(lines, n)
}

func plain(text string, n int) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = escaper.Replace(strings.TrimSuffix(l, "\r"))
	}
	return fit(lines, n)
}

// fit makes exactly n lines (a lexer may add or drop a final newline).
func fit(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

// HighlightText renders unsaved text (an editor's buffer) line by line, chosen by rel's file name. It
// reads nothing from disk.
func HighlightText(rel, src string) []string {
	return render(lexerFor(rel, src), src, strings.Count(src, "\n")+1)
}

// Doc is a file opened for viewing: its hash, language, line count and widest line, and its lines
// highlighted on demand.
type Doc struct {
	Hash    string
	Lang    string
	MaxCols int
	Total   int

	src    string
	starts []int // starts[i]: where line i begins; starts[Total] = len(src)+1
	lexer  chroma.Lexer
	key    string

	mu      sync.Mutex
	windows map[int][]string // highlighted windows by number
	whole   []string         // the whole file, once its background pass is done
	pass    bool             // a background pass was started
}

// Raw is line n (1-based) as it is in the file, without its line ending.
func (d *Doc) Raw(n int) string {
	if n < 1 || n > d.Total {
		return ""
	}
	return strings.TrimSuffix(d.src[d.starts[n-1]:d.starts[n]-1], "\r")
}

// Lines is lines [start, end) as HTML, and whether they are exact (from the whole file's pass, or a file
// with no highlighting) rather than from their window alone.
func (d *Doc) Lines(start, end int) ([]string, bool) {
	start, end = max(0, start), min(end, d.Total)
	if end <= start {
		return []string{}, true
	}
	d.mu.Lock()
	whole := d.whole
	d.mu.Unlock()
	if whole != nil {
		return append([]string(nil), whole[start:end]...), true
	}
	d.startPass()
	out := make([]string, 0, end-start)
	for w := start / chunkLines; w <= (end-1)/chunkLines; w++ {
		lines := d.window(w)
		from, to := max(start, w*chunkLines)-w*chunkLines, min(end, (w+1)*chunkLines)-w*chunkLines
		out = append(out, lines[from:to]...)
	}
	return out, d.lexer == nil
}

// Exact says whether every line is exact now (done), or will be once the background pass ends (coming).
func (d *Doc) Exact() (done, coming bool) {
	if d.lexer == nil {
		return true, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.whole != nil, d.whole == nil && len(d.src) <= fullPassBytes
}

// text is the source of lines [from, to).
func (d *Doc) text(from, to int) string {
	return d.src[d.starts[from] : d.starts[to]-1]
}

// window highlights window w on its own, with lines of context around it.
func (d *Doc) window(w int) []string {
	d.mu.Lock()
	if lines, ok := d.windows[w]; ok {
		d.mu.Unlock()
		return lines
	}
	d.mu.Unlock()
	start, end := w*chunkLines, min((w+1)*chunkLines, d.Total)
	from, to := max(0, start-leadLines), min(d.Total, end+tailLines)
	if len(d.text(from, to)) > maxLexBytes {
		from, to = start, end // too big with context: the window alone
	}
	var lines []string
	if d.lexer == nil || len(d.text(from, to)) > maxLexBytes {
		lines = plain(d.text(start, end), end-start)
	} else {
		lines = render(d.lexer, d.text(from, to), to-from)[start-from : end-from]
	}
	d.mu.Lock()
	if d.windows == nil {
		d.windows = map[int][]string{}
	}
	d.windows[w] = lines
	d.mu.Unlock()
	docs.charge(d.key, linesSize(lines))
	return lines
}

// startPass highlights a small file whole, once, in the background (two at a time).
var passSlots = make(chan struct{}, 2)

func (d *Doc) startPass() {
	if d.lexer == nil || len(d.src) > fullPassBytes {
		return
	}
	d.mu.Lock()
	if d.pass {
		d.mu.Unlock()
		return
	}
	d.pass = true
	d.mu.Unlock()
	go func() {
		passSlots <- struct{}{}
		defer func() { <-passSlots }()
		whole := render(d.lexer, d.text(0, d.Total), d.Total)
		d.mu.Lock()
		freed := 0
		for _, lines := range d.windows {
			freed += linesSize(lines)
		}
		d.whole, d.windows = whole, nil
		d.mu.Unlock()
		docs.charge(d.key, linesSize(whole)-freed)
	}()
}

func linesSize(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 16
	}
	return n
}

// Open reads a file for viewing. A document is kept (within a memory budget) until the file's size or
// modification time changes.
func Open(abs, rel string) (*Doc, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("is a directory")
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("file too large (%d bytes)", info.Size())
	}
	key := abs + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	if d := docs.get(key); d != nil {
		return d, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	f.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("file exceeds 64 MiB limit")
	}
	if int64(len(data)) != info.Size() {
		return nil, errors.New("file changed during read; retry")
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, errors.New("binary file")
	}
	d := newDoc(string(data), rel)
	d.key = key
	docs.put(key, d, len(data)+len(d.starts)*8)
	return d, nil
}

func newDoc(src, rel string) *Doc {
	d := &Doc{Hash: domain.Hash([]byte(src)), src: src, starts: []int{0}}
	width := 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\n':
			d.starts = append(d.starts, i+1)
			d.MaxCols = max(d.MaxCols, width)
			width = 0
		case '\t':
			width += 4 - width%4
		default:
			if src[i] < utf8.RuneSelf || utf8.RuneStart(src[i]) {
				width++
			}
		}
	}
	d.MaxCols = min(max(d.MaxCols, width), widthCap)
	d.starts = append(d.starts, len(src)+1)
	d.Total = len(d.starts) - 1
	d.lexer = lexerFor(rel, src)
	if d.lexer == nil {
		d.Lang = "plain text"
	} else {
		d.Lang = d.lexer.Config().Name
	}
	return d
}

// docs: open documents, least recently used first out, within cacheBytes.
var docs = &docCache{order: list.New(), byKey: map[string]*list.Element{}}

type docCache struct {
	mu    sync.Mutex
	order *list.List
	byKey map[string]*list.Element
	used  int
}

type cached struct {
	key  string
	doc  *Doc
	size int
}

func (c *docCache) get(key string) *Doc {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.byKey[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*cached).doc
	}
	return nil
}

func (c *docCache) put(key string, d *Doc, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another version of the same file goes.
	path := key[:strings.IndexByte(key, 0)]
	for k, e := range c.byKey {
		if strings.HasPrefix(k, path+"\x00") {
			c.used -= e.Value.(*cached).size
			c.order.Remove(e)
			delete(c.byKey, k)
		}
	}
	c.byKey[key] = c.order.PushFront(&cached{key: key, doc: d, size: size})
	c.used += size
	c.trim()
}

// charge adds what a document's rendered lines cost.
func (c *docCache) charge(key string, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.byKey[key]; ok {
		e.Value.(*cached).size += delta
		c.used += delta
		c.trim()
	}
}

func (c *docCache) trim() {
	for c.used > cacheBytes && c.order.Len() > 1 {
		e := c.order.Back()
		v := e.Value.(*cached)
		c.used -= v.size
		c.order.Remove(e)
		delete(c.byKey, v.key)
	}
}

// DiffRow is one line of a highlighted diff.
type DiffRow struct {
	Type    string `json:"type"`              // "ctx", "add" or "del"
	OldLine int    `json:"oldLine,omitempty"` // line in the old version
	NewLine int    `json:"newLine,omitempty"` // line in the new version
	At      int    `json:"at,omitempty"`      // for a deletion: the working-tree line it sits before
	Text    string `json:"text"`              // the line as it is
	HTML    string `json:"html,omitempty"`    // highlighted
}

// DiffHunk is one hunk of a highlighted diff.
type DiffHunk struct {
	OldStart int       `json:"oldStart"`
	NewStart int       `json:"newStart"`
	Section  string    `json:"section,omitempty"`
	Rows     []DiffRow `json:"rows"`
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$`)

// HighlightDiff parses a unified diff of one file into hunks with line numbers, highlighting each side
// of a hunk (old: context and deletions; new: context and additions) as code of rel's language.
func HighlightDiff(rel, diffText string) []DiffHunk {
	hunks := []DiffHunk{}
	var h *DiffHunk
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(diffText, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			hunks = append(hunks, DiffHunk{Section: m[3], Rows: []DiffRow{}})
			h = &hunks[len(hunks)-1]
			h.OldStart, _ = strconv.Atoi(m[1])
			h.NewStart, _ = strconv.Atoi(m[2])
			oldLine, newLine = h.OldStart, h.NewStart
			continue
		}
		if h == nil || line == "" || line[0] == '\\' {
			continue
		}
		text := line[1:]
		switch line[0] {
		case ' ':
			h.Rows = append(h.Rows, DiffRow{Type: "ctx", OldLine: oldLine, NewLine: newLine, Text: text})
			oldLine++
			newLine++
		case '-':
			h.Rows = append(h.Rows, DiffRow{Type: "del", OldLine: oldLine, At: newLine, Text: text})
			oldLine++
		case '+':
			h.Rows = append(h.Rows, DiffRow{Type: "add", NewLine: newLine, Text: text})
			newLine++
		}
	}
	lexer := lexerFor(rel, "")
	if lexer == nil {
		for i := range hunks {
			for j := range hunks[i].Rows {
				hunks[i].Rows[j].HTML = escaper.Replace(hunks[i].Rows[j].Text)
			}
		}
		return hunks
	}
	for i := range hunks {
		rows := hunks[i].Rows
		for _, side := range []string{"del", "add"} {
			var idx []int
			var text strings.Builder
			for j, r := range rows {
				if r.Type == "ctx" || r.Type == side {
					if len(idx) > 0 {
						text.WriteByte('\n')
					}
					text.WriteString(r.Text)
					idx = append(idx, j)
				}
			}
			if len(idx) == 0 {
				continue
			}
			for k, html := range render(lexer, text.String(), len(idx)) {
				if rows[idx[k]].Type == side || side == "add" {
					rows[idx[k]].HTML = html
				}
			}
		}
	}
	return hunks
}
