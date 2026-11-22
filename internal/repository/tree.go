package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type DirectoryEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Ignored   bool   `json:"ignored"`
	Protected bool   `json:"protected"`
}

// Directory returns only direct child metadata. It never walks descendants or
// opens file contents. Protected names are visible but cannot be traversed.
func (r *Repository) Directory(ctx context.Context, path string) ([]DirectoryEntry, error) {
	if path != "" {
		if _, err := r.ReadPath(path); err != nil {
			return nil, err
		}
	} else {
		path = "."
	}
	root, err := os.OpenRoot(r.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	children, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]DirectoryEntry, 0, len(children))
	var input strings.Builder
	for _, child := range children {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if child.Name() == ".git" {
			continue
		}
		p := filepath.ToSlash(filepath.Join(path, child.Name()))
		entry := DirectoryEntry{Path: p, Directory: child.IsDir(), Protected: Protected(p)}
		if child.Type()&os.ModeSymlink != 0 {
			// A link inside the folder opens like what it points at; one leading out is shown but closed.
			if target, ok := r.inside(filepath.Join(r.Path, p)); ok {
				if info, err := os.Stat(target); err == nil {
					entry.Directory = info.IsDir()
				}
			} else {
				entry.Protected = true
			}
		}
		result = append(result, entry)
		input.WriteString(p)
		if entry.Directory {
			input.WriteByte('/')
		}
		input.WriteByte(0)
	}
	if len(result) == 0 {
		return result, nil
	}
	// The ignore rules are held in memory (see Ignorer): no git process per folder opened.
	ig := r.Ignorer()
	ig.Sync()
	for i := range result {
		result[i].Ignored = ig.Ignored(result[i].Path, result[i].Directory)
	}
	return result, nil
}
