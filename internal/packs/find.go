package packs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Finding a language server. Installers put servers in their own folders, which are often not on the
// PATH Falcon was started with (an app launched from the Dock, a service, a shell without ~/go/bin):
// `go install` uses Go's bin folder, rustup ~/.cargo/bin, pipx ~/.local/bin, npm its global prefix,
// a distribution's LLVM a folder of its own. A project can also carry its own server (node_modules/.bin, a
// virtual environment). So a server is looked for in the project first, then on PATH, then in those
// folders, and found wherever the install command put it, without restarting Falcon.

// ProjectDirs are a project's own tool folders, searched first (the project's version of a tool wins).
func ProjectDirs(root string) []string {
	if root == "" {
		return nil
	}
	bin := "bin"
	if runtime.GOOS == "windows" {
		bin = "Scripts"
	}
	return []string{
		filepath.Join(root, "node_modules", ".bin"),
		filepath.Join(root, ".venv", bin),
		filepath.Join(root, "venv", bin),
	}
}

// ToolDirs are the folders installers put language servers in, for this system, that exist.
func ToolDirs() []string {
	home, _ := os.UserHomeDir()
	env := os.Getenv
	var dirs []string
	add := func(d ...string) {
		for _, x := range d {
			if x != "" {
				dirs = append(dirs, x)
			}
		}
	}
	join := func(base string, rest ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, rest...)...)
	}
	newest := func(pattern string) []string {
		m, _ := filepath.Glob(pattern)
		sort.Sort(sort.Reverse(sort.StringSlice(m)))
		return m
	}
	// Go
	add(env("GOBIN"))
	for _, p := range filepath.SplitList(env("GOPATH")) {
		add(join(p, "bin"))
	}
	add(join(home, "go", "bin"), join(env("GOROOT"), "bin"))
	add(newest(join(home, "sdk", "go*", "bin"))...) // go install golang.org/dl/goX.Y
	// Rust
	add(join(env("CARGO_HOME"), "bin"), join(home, ".cargo", "bin"))
	add(join(env("JAVA_HOME"), "bin"))
	// Node: npm's prefix, nvm, volta, pnpm, bun
	add(join(env("NPM_CONFIG_PREFIX"), "bin"), env("NVM_BIN"), env("PNPM_HOME"))
	add(join(home, ".npm-global", "bin"), join(home, ".volta", "bin"), join(home, ".bun", "bin"))
	add(newest(join(home, ".nvm", "versions", "node", "*", "bin"))...)
	// Python: pipx, uv tools, pip --user
	add(join(home, ".local", "bin"))
	switch runtime.GOOS {
	case "windows":
		add(join(env("APPDATA"), "npm"), join(home, "go", "bin"), join(env("ProgramFiles"), "Go", "bin"))
		add(join(env("ProgramFiles"), "LLVM", "bin"), join(home, "scoop", "shims"), join(env("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"))
		add(newest(join(env("APPDATA"), "Python", "Python*", "Scripts"))...)
		add(newest(join(env("LOCALAPPDATA"), "Programs", "Python", "Python*", "Scripts"))...)
	default:
		add("/usr/local/bin", "/usr/bin", "/snap/bin", "/usr/local/go/bin")
		add(newest("/usr/lib/go-*/bin")...) // Debian and Ubuntu's golang-1.xx
		add(newest("/usr/lib/llvm-*/bin")...)
		add(newest("/usr/lib/jvm/*/bin")...)
	}
	out, seen := []string{}, map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				out = append(out, d)
			}
		}
	}
	return out
}

// Look finds a command for a project: in the project's own tool folders (the project's, then the served
// folder's), then on PATH, then in the folders installers use.
func Look(roots ...string) func(string) (string, error) {
	return func(command string) (string, error) {
		for _, root := range roots {
			for _, d := range ProjectDirs(root) {
				if p, ok := executable(d, command); ok && trustProjectTool(root, p) {
					return p, nil
				}
			}
		}
		if p, err := exec.LookPath(command); err == nil {
			return p, nil
		}
		for _, d := range ToolDirs() {
			if p, ok := executable(d, command); ok {
				return p, nil
			}
		}
		return "", exec.ErrNotFound
	}
}

// trustProjectTool decides whether a language server found inside the project itself (root's
// node_modules/.bin, .venv or venv; path is the executable) may be started. Such a file comes with the
// project: in a fresh clone of someone else's repository it may have been committed by them, and it
// starts as soon as a file of its language is opened. Answering false falls through to PATH and the
// installers' folders.
//
// TODO(audit): choose the policy. Returning true keeps today's behaviour (the project's tool always wins).
func trustProjectTool(root, path string) bool {
	return true
}

// Remembered wraps a look so each command is looked for once: for one request that asks about every
// server (on Windows each look tries every PATHEXT ending in every PATH folder, so asking again and again
// took half a second). A fresh one per request still finds a server installed since.
func Remembered(look func(string) (string, error)) func(string) (string, error) {
	type answer struct {
		path string
		err  error
	}
	seen := map[string]answer{}
	return func(command string) (string, error) {
		if a, ok := seen[command]; ok {
			return a.path, a.err
		}
		p, err := look(command)
		seen[command] = answer{p, err}
		return p, err
	}
}

func executable(dir, command string) (string, bool) {
	names := []string{command}
	if runtime.GOOS == "windows" {
		names = []string{command + ".exe", command + ".cmd", command + ".bat", command}
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return p, true
		}
	}
	return "", false
}

// SearchPath is the PATH a language server runs with: the folder it was found in and the project's own
// tool folders first (a virtual environment's python, the project's node), then the owner's PATH, then the
// installers' folders (so gopls finds go and rust-analyzer finds cargo wherever they are).
func SearchPath(command string, roots ...string) string {
	parts := []string{}
	if command != "" {
		parts = append(parts, filepath.Dir(command))
	}
	for _, root := range roots {
		for _, d := range ProjectDirs(root) {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				parts = append(parts, d)
			}
		}
	}
	parts = append(parts, filepath.SplitList(os.Getenv("PATH"))...)
	parts = append(parts, ToolDirs()...)
	out, seen := []string{}, map[string]bool{}
	for _, p := range parts {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// Missing are the tools a server needs that are not on path (a PATH-style list, as SearchPath gives).
func (s Server) Missing(path string) []string {
	var out []string
	for _, need := range s.Needs {
		found := false
		for _, dir := range filepath.SplitList(path) {
			if _, ok := executable(dir, need); ok {
				found = true
				break
			}
		}
		if !found {
			out = append(out, need)
		}
	}
	return out
}

// Install is one way to install a server: the command, and where it puts the server (Falcon looks there).
type Install struct {
	Command string `json:"command"`
	Where   string `json:"where"`
}

// InstallsFor are a server's install commands for an operating system, best first.
// Falcon supports Windows and Linux; another Unix-like system is given Linux's.
func (s Server) InstallsFor(goos string) []Install {
	if list, ok := s.Installs[goos]; ok {
		return list
	}
	if list, ok := s.Installs["linux"]; ok && goos != "windows" {
		return list
	}
	return s.Installs["*"]
}

// ProjectRoot is where a file's language server should be rooted: the project the file belongs to,
// inside the folder Falcon serves (a folder can hold several: a Go module in a subfolder, a web app next
// to a server). It is the folder itself when no project file says otherwise.
func (p Pack) ProjectRoot(folder, file string) string {
	folder = filepath.Clean(folder)
	dir := filepath.Dir(filepath.Join(folder, filepath.FromSlash(file)))
	var chain []string // from the file's folder up to the served folder
	for d := dir; ; d = filepath.Dir(d) {
		rel, err := filepath.Rel(folder, d)
		if err != nil || strings.HasPrefix(rel, "..") {
			break
		}
		chain = append(chain, d)
		if d == folder {
			break
		}
	}
	has := func(d string, names ...string) bool {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				return true
			}
		}
		return false
	}
	contains := func(d, name, text string) bool {
		b, err := os.ReadFile(filepath.Join(d, name))
		return err == nil && strings.Contains(string(b), text)
	}
	nearest := func(names ...string) string {
		for _, d := range chain {
			if has(d, names...) {
				return d
			}
		}
		return ""
	}
	topmost := func(ok func(string) bool) string {
		for i := len(chain) - 1; i >= 0; i-- {
			if ok(chain[i]) {
				return chain[i]
			}
		}
		return ""
	}
	var root string
	switch p.Language {
	case "go":
		if root = topmost(func(d string) bool { return has(d, "go.work") }); root == "" {
			root = nearest("go.mod")
		}
	case "rust":
		if root = topmost(func(d string) bool { return contains(d, "Cargo.toml", "[workspace]") }); root == "" {
			root = nearest("Cargo.toml")
		}
	case "java":
		root = topmost(func(d string) bool {
			return has(d, "settings.gradle", "settings.gradle.kts") || contains(d, "pom.xml", "<modules>")
		})
		if root == "" {
			root = nearest("pom.xml", "build.gradle", "build.gradle.kts")
		}
	case "python":
		root = nearest("pyproject.toml", "setup.py", "setup.cfg", "pyrightconfig.json")
	case "typescript", "javascript":
		root = nearest("tsconfig.json", "jsconfig.json", "package.json")
	case "c", "cpp":
		root = nearest("compile_commands.json", filepath.Join("build", "compile_commands.json"))
	}
	if root == "" {
		return folder
	}
	return root
}
