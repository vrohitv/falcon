package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// Git handles nested .gitignore files, negation, tracked exceptions and
	// .git/info/exclude. Passing only direct children avoids recursive scanning.
	cmd, err := r.gitCommand(ctx, "check-ignore", "-z", "--stdin")
	if err != nil {
		return nil, err
	}
	cmd.Stdin = strings.NewReader(input.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, fmt.Errorf("read Git ignore rules: %s: %w", stderr.String(), err)
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(output), "\x00") {
		ignored[strings.TrimSuffix(p, "/")] = true
	}
	for i := range result {
		result[i].Ignored = ignored[result[i].Path]
	}
	return result, nil
}
