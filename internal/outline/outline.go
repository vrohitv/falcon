// Package outline finds what a file declares: Go precisely (its own parser), other languages by the shape of
// their declarations. It needs nothing outside the standard library.
package outline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// Definition is one declared name.
type Definition struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
}

var declarations = regexp.MustCompile(`^\s*(?:(?:export|default|public|private|protected|static|async|abstract|final|inline|virtual|const|pub(?:\([a-z]+\))?|unsafe|extern|override|sealed|open|internal)\s+)*(?:(def|class|interface|enum|function|struct|type|fn|trait|impl|mod|record|union|namespace)\s+([A-Za-z_$][\w$]*)|(?:[A-Za-z_][\w:<>,*&\[\] ]+)\s+([A-Za-z_$][\w$]*)\s*\()`)

var notNames = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "return": true, "else": true, "catch": true, "sizeof": true, "new": true, "delete": true}

// Of lists the file's declarations, in order.
func Of(path, content string) []Definition {
	if strings.HasSuffix(path, ".go") {
		if defs, ok := goOutline(path, content); ok {
			return defs
		}
	}
	out := []Definition{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".lock", ".csv", ".svg", ".html", ".css", "":
		return out
	}
	for i, line := range strings.Split(content, "\n") {
		if len(line) > 400 {
			continue
		}
		m := declarations.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kind, name := m[1], m[2]
		if name == "" {
			kind, name = "function", m[3]
		}
		if notNames[name] {
			continue
		}
		out = append(out, Definition{Name: name, Kind: kind, Path: path, Line: i + 1, Signature: strings.TrimSpace(line)})
	}
	return out
}

func goOutline(p, content string) ([]Definition, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, p, content, parser.SkipObjectResolution)
	if err != nil && file == nil {
		return nil, false
	}
	out := []Definition{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name, kind := d.Name.Name, "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name, kind = receiver(d.Recv.List[0].Type)+"."+name, "method"
			}
			out = append(out, Definition{Name: name, Kind: kind, Path: p, Line: fset.Position(d.Pos()).Line})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					out = append(out, Definition{Name: s.Name.Name, Kind: kind, Path: p, Line: fset.Position(s.Pos()).Line})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if n.Name != "_" {
							out = append(out, Definition{Name: n.Name, Kind: kind, Path: p, Line: fset.Position(n.Pos()).Line})
						}
					}
				}
			}
		}
	}
	return out, true
}

func receiver(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiver(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiver(t.X)
	case *ast.IndexListExpr:
		return receiver(t.X)
	}
	return "?"
}
