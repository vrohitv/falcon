package ide

import (
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// goModLexer reads go.mod and go.work files: directives, module paths, versions, comments. (Chroma has
// none; its "*.mod" pattern belongs to AMPL, which turned versions into errors.)
var goModLexer = chroma.MustNewLexer(
	&chroma.Config{Name: "Go Module", Filenames: []string{"go.mod", "go.work"}},
	func() chroma.Rules {
		return chroma.Rules{
			"root": {
				{Pattern: `//[^\n]*`, Type: chroma.CommentSingle},
				{Pattern: `^(module|go|toolchain|godebug|require|replace|exclude|retract|use|tool|ignore)\b`, Type: chroma.Keyword},
				{Pattern: `=>`, Type: chroma.Operator},
				{Pattern: `v\d+\.\d+\.\d+[\w.+-]*`, Type: chroma.LiteralNumber},
				{Pattern: `\b\d+\.\d+(\.\d+)?\b`, Type: chroma.LiteralNumber},
				{Pattern: `"[^"\n]*"|` + "`[^`]*`", Type: chroma.LiteralString},
				{Pattern: `[()\[\],]`, Type: chroma.Punctuation},
				{Pattern: `[^\s()\[\],"]+`, Type: chroma.NameNamespace},
				{Pattern: `\s+`, Type: chroma.TextWhitespace},
			},
		}
	},
)

// plainNames are files that look like a language to chroma's guesser but are not one.
var plainNames = map[string]bool{
	"go.sum": true, "go.work.sum": true, "yarn.lock": true, "package-lock.json": false, "pnpm-lock.yaml": false,
	".gitignore": true, ".gitattributes": true, ".dockerignore": true, ".npmignore": true, ".prettierignore": true,
	".eslintignore": true, "LICENSE": true, "LICENCE": true, "COPYING": true, "NOTICE": true, "AUTHORS": true,
	"CODEOWNERS": true, "OWNERS": true, ".editorconfig": false,
}

// lexerFor chooses the highlighter for a file: by its name first; a guess from the contents only for a
// script that says what it is (#!), since guessing anything else colours plain text as a random language.
func lexerFor(rel, src string) chroma.Lexer {
	base := filepath.Base(rel)
	switch {
	case base == "go.mod" || base == "go.work":
		return goModLexer
	case base == "Cargo.lock":
		return lexers.Get("toml")
	case plainNames[base]:
		return nil
	}
	if l := lexers.Match(base); l != nil {
		return l
	}
	if strings.HasPrefix(src, "#!") {
		return lexers.Analyse(src[:min(len(src), 16384)])
	}
	return nil
}
