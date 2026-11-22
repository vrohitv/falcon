//go:build !windows

package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// realPath is where p really is, every link resolved.
func realPath(p string) (string, error) { return filepath.EvalSymlinks(p) }

// protectedName reports whether one path component names Git's folder (macOS's usual filesystems ignore
// case, so ".GIT" opens .git there).
func protectedName(part string) bool {
	if runtime.GOOS == "darwin" {
		return strings.EqualFold(part, ".git")
	}
	return part == ".git"
}

// sameAsGitDir is for Windows' short names; other systems have none.
func sameAsGitDir(string, os.FileInfo) bool { return false }
