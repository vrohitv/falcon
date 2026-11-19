package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReviewFile is one file's changes, parsed from a unified diff.
type ReviewFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added, deleted, modified, renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	// Large: the diff is too big to show; open the file instead.
	Large bool `json:"large"`
	// Hash identifies this exact diff: a "viewed" mark is kept only while it stays the same.
	Hash string `json:"hash"`
	// Diff is the file's unified diff text (headers and hunks), for highlighting.
	Diff  string       `json:"-"`
	Hunks []ReviewHunk `json:"hunks"`
}

// ReviewHunk is one hunk's place and identity; the highlighted rows are added by the server.
type ReviewHunk struct {
	ID       string `json:"id"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Header   string `json:"-"`
	Body     string `json:"-"`
}

const (
	reviewFileLimit  = 256 << 10
	reviewTotalLimit = 12 << 20
)

// ParseDiff splits a multi-file unified diff into files and hunks. Very large diffs keep their file list
// but lose their hunks past the limit.
func ParseDiff(raw string) ([]ReviewFile, bool) {
	files := []ReviewFile{}
	total, truncated := 0, false
	chunks := strings.Split("\n"+raw, "\ndiff --git ")
	for _, chunk := range chunks[1:] {
		f := parseFileDiff("diff --git " + chunk)
		if f.Path == "" {
			continue
		}
		total += len(f.Diff)
		if total > reviewTotalLimit && !f.Large {
			f.Large, f.Hunks, f.Diff, truncated = true, []ReviewHunk{}, "", true
		}
		files = append(files, f)
	}
	return files, truncated
}

func unquote(p string) string {
	if strings.HasPrefix(p, "\"") {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

func parseFileDiff(text string) ReviewFile {
	f := ReviewFile{Status: "modified", Hunks: []ReviewHunk{}}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	header := true
	var hunk *ReviewHunk
	var body strings.Builder
	flush := func() {
		if hunk != nil {
			hunk.Body = body.String()
			sum := sha256.Sum256([]byte(f.Path + "\x00" + hunk.Header + "\x00" + hunk.Body))
			hunk.ID = hex.EncodeToString(sum[:8])
			f.Hunks = append(f.Hunks, *hunk)
		}
		body.Reset()
	}
	for _, line := range lines {
		if header {
			switch {
			case strings.HasPrefix(line, "--- "):
				if p := unquote(strings.TrimPrefix(line, "--- ")); p != "/dev/null" {
					f.OldPath = strings.TrimPrefix(p, "a/")
				}
				continue
			case strings.HasPrefix(line, "+++ "):
				if p := unquote(strings.TrimPrefix(line, "+++ ")); p != "/dev/null" {
					f.Path = strings.TrimPrefix(p, "b/")
				}
				continue
			case strings.HasPrefix(line, "new file mode"):
				f.Status = "added"
				continue
			case strings.HasPrefix(line, "deleted file mode"):
				f.Status = "deleted"
				continue
			case strings.HasPrefix(line, "rename from "):
				f.OldPath, f.Status = unquote(strings.TrimPrefix(line, "rename from ")), "renamed"
				continue
			case strings.HasPrefix(line, "rename to "):
				f.Path = unquote(strings.TrimPrefix(line, "rename to "))
				continue
			case strings.HasPrefix(line, "Binary files "):
				f.Binary = true
				continue
			case strings.HasPrefix(line, "@@ "):
				header = false
			default:
				continue
			}
		}
		if strings.HasPrefix(line, "@@ ") {
			flush()
			m := hunkHeader(line)
			hunk = &ReviewHunk{OldStart: m[0], OldLines: m[1], NewStart: m[2], NewLines: m[3], Header: line}
			continue
		}
		if hunk == nil {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
		if strings.HasPrefix(line, "+") {
			f.Additions++
		} else if strings.HasPrefix(line, "-") {
			f.Deletions++
		}
	}
	flush()
	// Names: the diff --git line, for files with no ---/+++ (binary, mode-only, empty new files).
	if f.Path == "" || f.OldPath == "" {
		first := lines[0]
		if rest := strings.TrimPrefix(first, "diff --git "); rest != first {
			if i := strings.Index(rest, " b/"); i > 0 && strings.HasPrefix(rest, "a/") {
				a, b := rest[2:i], rest[i+3:]
				if f.Path == "" {
					f.Path = unquote(b)
				}
				if f.OldPath == "" && f.Status != "added" {
					f.OldPath = unquote(a)
				}
			}
		}
	}
	if f.Status == "deleted" && f.Path == "" {
		f.Path = f.OldPath
	}
	if f.OldPath == f.Path {
		f.OldPath = ""
	}
	f.Diff = text
	if len(text) > reviewFileLimit {
		f.Large, f.Hunks, f.Diff = true, []ReviewHunk{}, ""
	}
	sum := sha256.Sum256([]byte(text))
	f.Hash = hex.EncodeToString(sum[:8])
	return f
}

func hunkHeader(line string) [4]int {
	var out [4]int
	out[1], out[3] = 1, 1
	m := strings.Fields(line)
	if len(m) < 3 {
		return out
	}
	parse := func(s string) (int, int) {
		s = s[1:]
		a, b, has := strings.Cut(s, ",")
		x, _ := strconv.Atoi(a)
		y := 1
		if has {
			y, _ = strconv.Atoi(b)
		}
		return x, y
	}
	out[0], out[1] = parse(m[1])
	out[2], out[3] = parse(m[2])
	return out
}

// applyHunk stages, unstages or discards one hunk of one file, identified by the ID the diff showed. The
// diff is computed again first: if the hunk is no longer exactly what was shown (the file changed since),
// nothing is applied.
func (r *Repository) applyHunk(ctx context.Context, req GitRequest, state GitState) (string, error) {
	if len(req.Paths) != 1 || req.Hunk == "" {
		return "", errors.New("choose one hunk")
	}
	path := req.Paths[0]
	if err := ValidPath(path); err != nil {
		return "", err
	}
	var file *GitFile
	for i := range state.Files {
		if state.Files[i].Path == path {
			file = &state.Files[i]
		}
	}
	if file == nil {
		return "", errors.New(path + " has no changes")
	}
	if file.Conflict {
		return "", errors.New(path + " has a merge conflict: resolve it first")
	}
	staged := req.Action == "hunk.unstage" || req.Action == "lines.unstage"
	raw, err := r.hunkDiff(ctx, path, staged, file.Worktree == "?")
	if err != nil {
		return "", err
	}
	files, _ := ParseDiff(raw)
	var target *ReviewFile
	var hunk *ReviewHunk
	for i := range files {
		for j := range files[i].Hunks {
			if files[i].Hunks[j].ID == req.Hunk {
				target, hunk = &files[i], &files[i].Hunks[j]
			}
		}
	}
	if hunk == nil {
		return "", errors.New("this change was edited since it was shown; look at it again")
	}
	// A new file is one hunk: staging adds it, discarding deletes it.
	if file.Worktree == "?" && strings.HasPrefix(req.Action, "hunk.") {
		switch req.Action {
		case "hunk.stage":
			_, err = r.clientGit(ctx, "", "add", "--", path)
			return "Staged " + path, err
		case "hunk.discard":
			root, e := os.OpenRoot(r.Path)
			if e != nil {
				return "", e
			}
			defer root.Close()
			return "Discarded " + path + " (deleted)", root.Remove(path)
		}
	}
	body, header := hunk.Body, hunk.Header
	if strings.HasPrefix(req.Action, "lines.") {
		reverse := req.Action != "lines.stage"
		var ok bool
		header, body, ok = partialHunk(hunk, req.Lines, reverse)
		if !ok {
			return "", errors.New("choose added or removed lines in this change")
		}
	}
	fileHead := fileHeader(target.Diff)
	if file.Worktree == "?" && req.Action == "lines.stage" {
		// Part of a new file: it enters the index as new, with only the chosen lines.
		if _, err := r.clientGit(ctx, "", "add", "--intent-to-add", "--", path); err != nil {
			return "", err
		}
		var e error
		if raw, e = r.hunkDiff(ctx, path, false, false); e != nil {
			return "", e
		}
		fileHead = fileHeader(raw)
	}
	patch := fileHead + header + "\n" + body
	var args []string
	switch req.Action {
	case "hunk.stage", "lines.stage":
		args = []string{"apply", "--cached", "--whitespace=nowarn", "--recount", "-"}
	case "hunk.unstage", "lines.unstage":
		args = []string{"apply", "--cached", "--reverse", "--whitespace=nowarn", "--recount", "-"}
	case "hunk.discard", "lines.discard":
		args = []string{"apply", "--reverse", "--whitespace=nowarn", "--recount", "-"}
	}
	cmd, err := r.gitCommand(ctx, args...)
	if err != nil {
		return "", err
	}
	cmd.Stdin = strings.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("the change no longer applies cleanly (%s); look at it again", strings.TrimSpace(stderr.String()))
	}
	verb := map[string]string{"hunk.stage": "Staged", "hunk.unstage": "Unstaged", "hunk.discard": "Discarded", "lines.stage": "Staged", "lines.unstage": "Unstaged", "lines.discard": "Discarded"}[req.Action]
	if strings.HasPrefix(req.Action, "lines.") {
		n := len(req.Lines)
		return fmt.Sprintf("%s %d line%s in %s", verb, n, map[bool]string{true: "", false: "s"}[n == 1], path), nil
	}
	return fmt.Sprintf("%s a change in %s", verb, path), nil
}

// partialHunk keeps only the chosen lines of a hunk (indices of its shown lines), as git add -p's line editing
// does. Applied forwards (staging), an unchosen removal stays as context and an unchosen addition is left
// out; applied in reverse (unstaging, discarding), the other way round. The counts in the header are
// rewritten to match.
func partialHunk(h *ReviewHunk, chosen []int, reverse bool) (string, string, bool) {
	pick := map[int]bool{}
	for _, i := range chosen {
		pick[i] = true
	}
	lines := strings.Split(strings.TrimSuffix(h.Body, "\n"), "\n")
	var b strings.Builder
	oldN, newN, changes := 0, 0, 0
	// Indices count the hunk's lines as shown: "\ No newline at end of file" markers are not counted.
	i := -1
	for _, l := range lines {
		if l == "" {
			l = " "
		}
		if l[0] != '\\' {
			i++
		}
		switch l[0] {
		case '+':
			if pick[i] {
				b.WriteString(l + "\n")
				newN++
				changes++
			} else if reverse {
				b.WriteString(" " + l[1:] + "\n")
				oldN++
				newN++
			}
		case '-':
			if pick[i] {
				b.WriteString(l + "\n")
				oldN++
				changes++
			} else if !reverse {
				b.WriteString(" " + l[1:] + "\n")
				oldN++
				newN++
			}
		case '\\':
			b.WriteString(l + "\n")
		default:
			b.WriteString(l + "\n")
			oldN++
			newN++
		}
	}
	if changes == 0 {
		return "", "", false
	}
	section := ""
	if i := strings.Index(h.Header[2:], "@@"); i >= 0 {
		section = h.Header[2+i+2:]
	}
	header := fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", h.OldStart, oldN, h.NewStart, newN, section)
	return header, b.String(), true
}

// hunkDiff is the diff hunks of one file are taken from: staged (HEAD against the index) or not.
func (r *Repository) hunkDiff(ctx context.Context, path string, staged, untracked bool) (string, error) {
	if staged {
		return r.clientGit(ctx, "", "diff", "--cached", "--no-ext-diff", "--no-textconv", "--unified=3", "--", path)
	}
	return r.unstagedPathDiff(ctx, path, untracked)
}

// HunkIDs are the IDs of a file's hunks, in order, as the Source Control diff shows them.
func (r *Repository) HunkIDs(ctx context.Context, path, area string) ([]string, error) {
	if err := ValidPath(path); err != nil {
		return nil, err
	}
	untracked := false
	if area == "working" {
		out, err := r.clientGit(ctx, "", "ls-files", "--", path)
		if err != nil {
			return nil, err
		}
		untracked = out == ""
	}
	raw, err := r.hunkDiff(ctx, path, area == "staged", untracked)
	if err != nil {
		return nil, err
	}
	files, _ := ParseDiff(raw)
	ids := []string{}
	for _, f := range files {
		for _, h := range f.Hunks {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

// unstagedPathDiff is one file's index-to-working-tree diff; a new file is diffed as added.
func (r *Repository) unstagedPathDiff(ctx context.Context, path string, untracked bool) (string, error) {
	if !untracked {
		return r.clientGit(ctx, "", "diff", "--no-ext-diff", "--no-textconv", "--unified=3", "--", path)
	}
	if _, err := r.securePath(path); err != nil {
		return "", err
	}
	// The file is named relative to the repository (git runs there), so the diff names it the same way
	// on every system (an absolute path comes back quoted with backslashes on Windows).
	cmd, err := r.gitCommand(ctx, "diff", "--no-ext-diff", "--no-textconv", "--no-index", "--unified=3", "--", "/dev/null", path)
	if err != nil {
		return "", err
	}
	out, _ := cmd.Output()
	return string(out), nil
}

// fileHeader is a file diff's lines before its first hunk.
func fileHeader(diff string) string {
	if i := strings.Index(diff, "\n@@ "); i >= 0 {
		return diff[:i+1]
	}
	return diff
}
