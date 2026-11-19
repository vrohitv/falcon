package ide

import (
	"path/filepath"
	"strings"
)

// FileEntry is a path prepared for go-to-file: lowercased once, with where its name starts.
type FileEntry struct {
	Path      string
	Name      string
	lower     string
	nameStart int
}

// Entries prepares paths for FuzzyFind.
func Entries(paths []string) []FileEntry {
	out := make([]FileEntry, len(paths))
	for i, p := range paths {
		out[i] = FileEntry{Path: p, Name: filepath.Base(p), lower: strings.ToLower(p), nameStart: strings.LastIndex(p, "/") + 1}
	}
	return out
}
