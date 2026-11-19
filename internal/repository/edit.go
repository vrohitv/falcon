package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vrohitv/falcon/internal/domain"
)

func (r *Repository) ReadWindow(path string, offset, limit int) (string, error) {
	if offset < 0 || limit < 0 || limit > 2000 {
		return "", errors.New("offset must be nonnegative and limit must be 1–2000")
	}
	if limit == 0 {
		limit = 200
	}
	f, err := r.Read(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(f.Content, "\n")
	if offset >= len(lines) {
		return "", fmt.Errorf("offset %d is past the %d lines in this file", offset, len(lines))
	}
	end := min(offset+limit, len(lines))
	var out strings.Builder
	fmt.Fprintf(&out, "sha256: %s\npath: %s\nlines: %d–%d of %d (offset is zero-based)\n\n", f.Hash, path, offset+1, end, len(lines))
	for i := offset; i < end; i++ {
		fmt.Fprintf(&out, "%d: %s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "[%d more lines; continue with offset=%d]\n", len(lines)-end, end)
	}
	return out.String(), nil
}

func (r *Repository) Edit(path, old, new, expectedHash string) (FileContent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.Read(path)
	if err != nil {
		return f, err
	}
	if f.Hash != expectedHash {
		return FileContent{}, domain.ErrConflict
	}
	if old == "" || strings.Count(f.Content, old) != 1 {
		return FileContent{}, errors.New("old_string must match exactly once; include enough surrounding text")
	}
	return r.write(path, strings.Replace(f.Content, old, new, 1), expectedHash)
}
func (r *Repository) Delete(path, expectedHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.Read(path)
	if err != nil {
		return err
	}
	if f.Hash != expectedHash {
		return domain.ErrConflict
	}
	full, err := r.securePath(path)
	if err != nil {
		return err
	}
	if err = os.Remove(full); err != nil {
		return err
	}
	return syncDir(filepath.Dir(full))
}
func (r *Repository) Move(path, destination, expectedHash string) (FileContent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.Read(path)
	if err != nil {
		return f, err
	}
	if f.Hash != expectedHash {
		return FileContent{}, domain.ErrConflict
	}
	source, err := r.securePath(path)
	if err != nil {
		return FileContent{}, err
	}
	target, err := r.securePath(destination)
	if err != nil {
		return FileContent{}, err
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		return FileContent{}, errors.New("destination must not exist")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return FileContent{}, err
	}
	if err = os.Rename(source, target); err != nil {
		return FileContent{}, err
	}
	if err = syncDir(filepath.Dir(source)); err != nil {
		return FileContent{}, err
	}
	if err = syncDir(filepath.Dir(target)); err != nil {
		return FileContent{}, err
	}
	f.Path = destination
	return f, nil
}
func syncDir(path string) error {
	// Windows cannot open a folder to flush it; the file itself was already flushed.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
