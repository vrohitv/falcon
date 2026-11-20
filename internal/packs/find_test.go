package packs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestProjectRootsInsideOneFolder(t *testing.T) {
	f := t.TempDir()
	write(t, filepath.Join(f, "go.mod"), "module top\n")
	write(t, filepath.Join(f, "tools", "go.mod"), "module tools\n")
	write(t, filepath.Join(f, "web", "package.json"), "{}")
	write(t, filepath.Join(f, "crates", "Cargo.toml"), "[workspace]\n")
	write(t, filepath.Join(f, "crates", "a", "Cargo.toml"), "[package]\n")
	write(t, filepath.Join(f, "svc", "pyproject.toml"), "")
	go_, _ := PackFor("go")
	ts, _ := PackFor("typescript")
	rs, _ := PackFor("rust")
	py, _ := PackFor("python")
	cases := []struct {
		pack       Pack
		file, want string
	}{
		{go_, "main.go", f},
		{go_, "internal/x/x.go", f},
		{go_, "tools/cmd/main.go", filepath.Join(f, "tools")},
		{ts, "web/src/app.ts", filepath.Join(f, "web")},
		{ts, "scripts/x.ts", f},
		{rs, "crates/a/src/lib.rs", filepath.Join(f, "crates")},
		{py, "svc/app/main.py", filepath.Join(f, "svc")},
		{py, "other.py", f},
	}
	for _, c := range cases {
		if got := c.pack.ProjectRoot(f, c.file); got != c.want {
			t.Errorf("%s %s: got %s, want %s", c.pack.Language, c.file, got, c.want)
		}
	}
	// A go.work above modules roots them all together.
	write(t, filepath.Join(f, "go.work"), "go 1.22\nuse (. ./tools)\n")
	if got := go_.ProjectRoot(f, "tools/cmd/main.go"); got != f {
		t.Errorf("go.work: got %s", got)
	}
}

func TestLookFindsProjectAndInstallerFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bits")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "node_modules", ".bin", "typescript-language-server"), "#!/bin/sh\n")
	write(t, filepath.Join(root, ".venv", "bin", "pylsp"), "#!/bin/sh\n")
	if p, err := Look(root)("typescript-language-server"); err != nil || p != filepath.Join(root, "node_modules", ".bin", "typescript-language-server") {
		t.Fatalf("project node_modules: %s %v", p, err)
	}
	if p, err := Look(root)("pylsp"); err != nil || p != filepath.Join(root, ".venv", "bin", "pylsp") {
		t.Fatalf("project venv: %s %v", p, err)
	}
	// A server installed to Go's bin folder is found even when that folder is not on PATH.
	home := t.TempDir()
	write(t, filepath.Join(home, "go", "bin", "gopls"), "#!/bin/sh\n")
	t.Setenv("HOME", home)
	t.Setenv("GOPATH", "")
	t.Setenv("GOBIN", "")
	t.Setenv("PATH", "/nonexistent")
	if p, err := Look(root)("gopls"); err != nil || p != filepath.Join(home, "go", "bin", "gopls") {
		t.Fatalf("~/go/bin: %s %v", p, err)
	}
	if _, err := Look(root)("no-such-server"); err == nil {
		t.Fatal("found a server that does not exist")
	}
	// The server's PATH starts with its own folder and the project's tool folders.
	path := filepath.SplitList(SearchPath(filepath.Join(home, "go", "bin", "gopls"), root))
	if path[0] != filepath.Join(home, "go", "bin") || path[1] != filepath.Join(root, "node_modules", ".bin") {
		t.Fatalf("search path: %v", path[:3])
	}
}

func TestEveryServerHasInstallCommandsForEverySystem(t *testing.T) {
	for _, p := range Packs {
		for _, s := range p.Servers {
			for _, goos := range []string{"linux", "windows"} {
				list := s.InstallsFor(goos)
				if len(list) == 0 {
					t.Errorf("%s has no install command for %s", s.Command, goos)
				}
				for _, in := range list {
					if in.Command == "" || in.Where == "" {
						t.Errorf("%s on %s: %+v", s.Command, goos, in)
					}
				}
			}
		}
	}
}

func TestMissingToolsAreReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bits")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go"), "#!/bin/sh\n")
	if m := gopls.Missing(dir); len(m) != 0 {
		t.Fatalf("go is there: %v", m)
	}
	if m := gopls.Missing(t.TempDir()); len(m) != 1 || m[0] != "go" {
		t.Fatalf("go is missing: %v", m)
	}
	if m := clangd.Missing(""); len(m) != 0 {
		t.Fatalf("clangd needs nothing: %v", m)
	}
}
