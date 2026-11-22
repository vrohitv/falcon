//go:build windows

package repository

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// realPath is where p really is: every symbolic link, junction, substituted drive and short (8.3) name
// resolved by the system itself. filepath.EvalSymlinks leaves junctions alone, so it cannot tell whether
// one leads out of the folder.
func realPath(p string) (string, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	// No access asked for: the handle only names the file. Backup semantics opens folders too.
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: p, Err: err}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0 /* FILE_NAME_NORMALIZED | VOLUME_NAME_DOS */)
		if err != nil {
			return "", &os.PathError{Op: "resolve", Path: p, Err: err}
		}
		if int(n) < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n) // n is the size needed, its NUL included
	}
	s := windows.UTF16ToString(buf)
	switch {
	case strings.HasPrefix(s, `\\?\UNC\`):
		s = `\\` + s[len(`\\?\UNC\`):]
	case strings.HasPrefix(s, `\\?\`):
		s = s[len(`\\?\`):]
	}
	return filepath.Clean(s), nil
}

// protectedName reports whether one path component names Git's folder. Windows ignores case and drops
// trailing dots and spaces, so ".GIT", ".git." and ".git " all open .git there (short names such as
// GIT~1 are caught by sameAsGitDir).
func protectedName(part string) bool {
	return strings.EqualFold(strings.TrimRight(part, ". "), ".git")
}

// sameAsGitDir reports whether the existing entry at abs is its folder's .git under another name: a
// short (8.3) name like GIT~1, which only a comparison of the files themselves can see through.
func sameAsGitDir(abs string, info os.FileInfo) bool {
	if !strings.Contains(filepath.Base(abs), "~") {
		return false
	}
	git, err := os.Lstat(filepath.Join(filepath.Dir(abs), ".git"))
	return err == nil && os.SameFile(info, git)
}
