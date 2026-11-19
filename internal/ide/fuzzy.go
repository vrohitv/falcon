package ide

import (
	"sort"
	"strings"
)

// FuzzyResult is one file matched by go-to-file, with where the query's characters were found.
type FuzzyResult struct {
	Path  string `json:"path"` // path in the folder
	Name  string `json:"name"` // file name
	Pos   []int  `json:"pos"`  // byte offsets in Path of the matched characters (for highlighting)
	score int
}

// FuzzyFind ranks files against a typed query. The query's characters must appear in order, ignoring
// case; a match inside the file name beats one spread over folders, and tight matches on word starts beat
// scattered ones. Spaces in the query are ignored.
func FuzzyFind(files []FileEntry, query string, limit int) []FuzzyResult {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	q := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(query), " ", ""))
	if q == "" {
		return []FuzzyResult{}
	}
	out := make([]FuzzyResult, 0, 64)
	for i := range files {
		f := &files[i]
		if !inOrder(f.lower, q, 0) {
			continue
		}
		var pos []int
		score := 0
		if inOrder(f.lower, q, f.nameStart) {
			pos = tightest(f.lower, q, f.nameStart)
			score = 10000 + rate(f.Path, f.lower, q, pos, f.nameStart)
		} else {
			pos = tightest(f.lower, q, 0)
			score = rate(f.Path, f.lower, q, pos, f.nameStart)
		}
		out = append(out, FuzzyResult{Path: f.Path, Name: f.Name, Pos: pos, score: score})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		if len(out[a].Path) != len(out[b].Path) {
			return len(out[a].Path) < len(out[b].Path)
		}
		return out[a].Path < out[b].Path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// inOrder reports whether every byte of q occurs in s, in order, from byte from on.
func inOrder(s, q string, from int) bool {
	j := 0
	for i := from; i < len(s) && j < len(q); i++ {
		if s[i] == q[j] {
			j++
		}
	}
	return j == len(q)
}

// tightest finds where q occurs in s (from byte from on) as compactly as it can: the first place the
// whole query can end, then, walking back from there, the latest place it can start. It then prefers word
// starts where moving a character to one keeps the order.
func tightest(s, q string, from int) []int {
	end, j := -1, 0
	for i := from; i < len(s); i++ {
		if s[i] == q[j] {
			j++
			if j == len(q) {
				end = i
				break
			}
		}
	}
	pos := make([]int, len(q))
	j = len(q) - 1
	for i := end; i >= from && j >= 0; i-- {
		if s[i] == q[j] {
			pos[j] = i
			j--
		}
	}
	// A character found mid-word may have a word start between its neighbours: use that instead.
	for k := range pos {
		if wordStart(s, pos[k]) {
			continue
		}
		lo := from
		if k > 0 {
			lo = pos[k-1] + 1
		}
		hi := len(s) - 1
		if k+1 < len(pos) {
			hi = pos[k+1] - 1
		}
		for i := lo; i <= hi; i++ {
			if s[i] == q[k] && wordStart(s, i) {
				pos[k] = i
				break
			}
		}
	}
	return pos
}

// wordStart: the first byte, or one after a separator.
func wordStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	switch s[i-1] {
	case '/', '_', '-', '.', ' ', '@':
		return true
	}
	return false
}

// rate scores a match: consecutive characters and word starts (including camelCase, read from the
// original path) count for it, gaps and long paths against it; a name the query begins or equals wins.
func rate(path, lower, q string, pos []int, nameStart int) int {
	score := 0
	for k, p := range pos {
		if wordStart(lower, p) || (p > 0 && isUpper(path[p]) && !isUpper(path[p-1])) {
			score += 30
		}
		if k > 0 && pos[k-1] == p-1 {
			score += 40
		}
		if k > 0 {
			score -= min(pos[k]-pos[k-1]-1, 20)
		}
	}
	name := lower[nameStart:]
	switch {
	case name == q || strings.TrimSuffix(name, extOf(name)) == q:
		score += 500
	case strings.HasPrefix(name, q):
		score += 250
	case pos[0] == nameStart:
		score += 60
	}
	score -= len(path) / 4
	return score
}

func isUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}
