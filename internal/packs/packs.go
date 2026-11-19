package packs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Pack is one language's support: which files, which language server (the first of Servers that is
// installed), how to recognise its projects (C, C++, Python, TypeScript, JavaScript, Go, Java, Rust).
type Pack struct {
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`
	// Server and Args are the preferred server, kept for what reads them; Servers lists every one that
	// will do, best first.
	Server     string   `json:"server"`
	Args       []string `json:"args"`
	Servers    []Server `json:"servers"`
	BuildFiles []string `json:"build_files"`
	Checks     []string `json:"suggested_checks"`
}

// Server is one language server that can serve a pack. In Args, {data} stands for a directory of its own
// that persists between runs (jdtls keeps its workspace index there).
type Server struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Install string   `json:"install"`
	// Navigates: it answers definition and references (ruff, a linter, only reports problems).
	Navigates bool `json:"navigates"`
	// Slow: it reads the whole project before answering (minutes for a large Java or Rust one).
	Slow bool `json:"slow,omitempty"`
	// Find, when set, decides whether this server serves a repository and which executable runs it
	// (instead of looking Command up on PATH).
	Find func(root string, look func(string) (string, error)) (string, bool) `json:"-"`
}

func pack(language string, ext []string, build, checks []string, servers ...Server) Pack {
	return Pack{Language: language, Extensions: ext, Server: servers[0].Command, Args: servers[0].Args, Servers: servers, BuildFiles: build, Checks: checks}
}

var (
	gopls    = Server{Command: "gopls", Args: []string{}, Install: "go install golang.org/x/tools/gopls@latest", Navigates: true}
	clangd   = Server{Command: "clangd", Args: []string{"--background-index", "--header-insertion=never"}, Install: "brew install llvm  # or: apt install clangd", Navigates: true}
	tsserver = Server{Command: "typescript-language-server", Args: []string{"--stdio"}, Install: "npm install -g typescript-language-server typescript", Navigates: true}
	// TypeScript 7 (the native compiler) is its own language server: tsc --lsp.
	tsNative = Server{Command: "tsc", Args: []string{"--lsp", "--stdio"}, Install: "npm install --save-dev typescript@7  # or: npm install -g typescript@7", Navigates: true, Find: nativeTypeScript}
	jdtls    = Server{Command: "jdtls", Args: []string{"-data", "{data}"}, Install: "brew install jdtls  # needs Java 25 or newer (JAVA_HOME)", Navigates: true, Slow: true}
	ra       = Server{Command: "rust-analyzer", Args: []string{}, Install: "rustup component add rust-analyzer", Navigates: true, Slow: true}
)

// Packs are the languages Falcon supports end to end: C, C++, Python, TypeScript, JavaScript, Go, Java, Rust.
var Packs = []Pack{
	pack("go", []string{".go"}, []string{"go.mod", "go.work"}, []string{"go test ./...", "go vet ./..."}, gopls),
	pack("python", []string{".py", ".pyi"}, []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg"}, []string{"python -m pytest", "ruff check ."},
		Server{Command: "basedpyright-langserver", Args: []string{"--stdio"}, Install: "pip install basedpyright  # or: npm install -g basedpyright", Navigates: true},
		Server{Command: "pyright-langserver", Args: []string{"--stdio"}, Install: "npm install -g pyright  # or: pip install pyright", Navigates: true},
		Server{Command: "pylsp", Args: []string{}, Install: "pip install python-lsp-server", Navigates: true},
		Server{Command: "ruff", Args: []string{"server"}, Install: "pip install ruff  # problems only, no navigation"}),
	pack("c", []string{".c", ".h"}, []string{"compile_commands.json", "CMakeLists.txt", "Makefile", "meson.build"}, []string{"cmake --build build", "ctest --test-dir build"}, clangd),
	pack("cpp", []string{".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx"}, []string{"compile_commands.json", "CMakeLists.txt", "Makefile", "meson.build"}, []string{"cmake --build build", "ctest --test-dir build"}, clangd),
	pack("typescript", []string{".ts", ".tsx", ".mts", ".cts"}, []string{"tsconfig.json", "package.json"}, []string{"npm test", "npx tsc --noEmit"}, tsNative, tsserver),
	pack("javascript", []string{".js", ".jsx", ".mjs", ".cjs"}, []string{"jsconfig.json", "package.json"}, []string{"npm test"}, tsNative, tsserver),
	pack("java", []string{".java"}, []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle"}, []string{"mvn test", "./gradlew test"}, jdtls),
	pack("rust", []string{".rs"}, []string{"Cargo.toml"}, []string{"cargo test", "cargo clippy"}, ra),
}

// PackFor is the pack for a language, if there is one.
func PackFor(language string) (Pack, bool) {
	for _, p := range Packs {
		if p.Language == language {
			return p, true
		}
	}
	return Pack{}, false
}

// Resolve is the first of the pack's servers that is installed (looked up with `look`, exec.LookPath in
// use), or false when none is.
func (p Pack) Resolve(look func(string) (string, error)) (Server, string, bool) {
	return p.ResolveIn("", look)
}

// ResolveIn is Resolve for a repository: some servers depend on what the project itself installed.
func (p Pack) ResolveIn(root string, look func(string) (string, error)) (Server, string, bool) {
	for _, s := range p.Servers {
		if s.Find != nil {
			if path, ok := s.Find(root, look); ok {
				return s, path, true
			}
			continue
		}
		if path, err := look(s.Command); err == nil {
			return s, path, true
		}
	}
	return Server{}, "", false
}

// nativeTypeScript finds a TypeScript 7 or newer to run as the language server: the project's own when
// it has one (an older one in the project means typescript-language-server serves it instead), otherwise
// the tsc on PATH when that is TypeScript 7.
func nativeTypeScript(root string, look func(string) (string, error)) (string, bool) {
	if root != "" {
		if v, ok := typeScriptMajor(filepath.Join(root, "node_modules", "typescript")); ok {
			return filepath.Join(root, "node_modules", ".bin", "tsc"), v >= 7
		}
	}
	tsc, err := look("tsc")
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(tsc)
	if err != nil {
		return "", false
	}
	// …/node_modules/typescript/bin/tsc
	if v, ok := typeScriptMajor(filepath.Dir(filepath.Dir(real))); ok && v >= 7 {
		return tsc, true
	}
	return "", false
}

// typeScriptMajor reads the major version of the TypeScript package in dir.
func typeScriptMajor(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return 0, false
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pkg) != nil || pkg.Name != "typescript" {
		return 0, false
	}
	major, err := strconv.Atoi(strings.SplitN(pkg.Version, ".", 2)[0])
	return major, err == nil
}

// LanguageID is what the LSP calls a file's language (a .tsx file is typescriptreact, not typescript).
func LanguageID(path, language string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	}
	return language
}
