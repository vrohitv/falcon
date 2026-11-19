package repository

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type GitCommit struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Message string   `json:"message"`
}
type GitLog struct {
	Head    string      `json:"head"`
	Commits []GitCommit `json:"commits"`
	Next    int         `json:"next"`
	Backend string      `json:"backend"`
}

// native is a go-git handle on the repository, opened once and kept: opening reads the pack indexes
// (180 ms on a repository the size of Kubernetes), after which a commit is found in microseconds without
// starting a process. It is used only for what go-git does fast (single commits, the newest pages of
// history); anything it fails at is asked of git, and the handle is opened afresh next time (a gc may
// have replaced the packs it knew).
func (r *Repository) native() *git.Repository {
	r.gogit.Lock()
	defer r.gogit.Unlock()
	if r.gogit.repo == nil {
		repo, err := git.PlainOpenWithOptions(r.Path, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
		if err != nil {
			return nil
		}
		r.gogit.repo = repo
	}
	return r.gogit.repo
}

func (r *Repository) dropNative() {
	r.gogit.Lock()
	r.gogit.repo = nil
	r.gogit.Unlock()
}

func commitView(c *object.Commit) GitCommit {
	parents := make([]string, 0, len(c.ParentHashes))
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	subject, _, _ := strings.Cut(c.Message, "\n")
	return GitCommit{Hash: c.Hash.String(), Parents: parents, Author: c.Author.Name, Email: c.Author.Email, Date: c.Author.When.Format(time.RFC3339), Subject: subject, Message: c.Message}
}

// nativeLog walks history newest first in-process; ok is false when git should be asked instead.
func (r *Repository) nativeLog(ctx context.Context, head string, offset, limit int) (commits []GitCommit, more, ok bool) {
	repo := r.native()
	if repo == nil || len(head) != 40 {
		return nil, false, false
	}
	r.gogit.use.Lock()
	defer r.gogit.use.Unlock()
	iter, err := repo.Log(&git.LogOptions{From: plumbing.NewHash(head), Order: git.LogOrderCommitterTime})
	if err != nil {
		r.dropNative()
		return nil, false, false
	}
	defer iter.Close()
	commits = []GitCommit{}
	for i := 0; i < offset+limit+1; i++ {
		if ctx.Err() != nil {
			return nil, false, false
		}
		c, err := iter.Next()
		if err == io.EOF {
			return commits, false, true
		}
		if err != nil {
			r.dropNative()
			return nil, false, false
		}
		if i < offset {
			continue
		}
		if len(commits) == limit {
			return commits, true, true
		}
		commits = append(commits, commitView(c))
	}
	return commits, false, true
}

// CommitLog is a page of history from head, newest first. The newest pages come from go-git (2 ms for
// a hundred commits, against 10 ms for starting git, and far more on Windows); deep pages from git,
// which reads the commit-graph file and is the quicker there.
func (r *Repository) CommitLog(ctx context.Context, head string, offset, limit int) (GitLog, error) {
	result := GitLog{Head: head, Commits: []GitCommit{}, Next: -1, Backend: "git"}
	if offset < 0 || offset > 100000 || limit < 1 || limit > 100 {
		return result, errors.New("invalid history page")
	}
	if head == "" {
		if repo := r.native(); repo != nil {
			r.gogit.use.Lock()
			if ref, err := repo.Head(); err == nil {
				head = ref.Hash().String()
			}
			r.gogit.use.Unlock()
		}
		if head == "" {
			value, err := r.clientGit(ctx, "", "rev-parse", "--verify", "HEAD")
			if err != nil {
				return result, nil
			}
			head = strings.TrimSpace(value)
		}
		result.Head = head
	}
	if !ValidCommit(head) {
		return result, errors.New("history head must be a full commit hash")
	}
	if offset < 1000 {
		if commits, more, ok := r.nativeLog(ctx, head, offset, limit); ok {
			result.Commits, result.Backend = commits, "go-git"
			if more {
				result.Next = offset + limit
			}
			return result, nil
		}
	}
	raw, err := r.clientGit(ctx, "", "log", "--no-show-signature", "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%s%x00%B%x00", "--skip="+strconv.Itoa(offset), "-n", strconv.Itoa(limit+1), head, "--")
	if err != nil {
		return result, err
	}
	parts := strings.Split(raw, "\x00")
	for i := 0; i+6 < len(parts); i += 7 {
		c := GitCommit{Hash: strings.TrimSpace(parts[i]), Parents: strings.Fields(parts[i+1]), Author: parts[i+2], Email: parts[i+3], Date: parts[i+4], Subject: parts[i+5], Message: parts[i+6]}
		if c.Hash == "" {
			continue
		}
		if len(result.Commits) == limit {
			result.Next = offset + limit
			break
		}
		result.Commits = append(result.Commits, c)
	}
	return result, nil
}

// commitInfo is one commit, from go-git when it can (no process), else from git.
func (r *Repository) commitInfo(ctx context.Context, hash string) (GitCommit, error) {
	if repo := r.native(); repo != nil && len(hash) == 40 {
		r.gogit.use.Lock()
		c, err := repo.CommitObject(plumbing.NewHash(hash))
		r.gogit.use.Unlock()
		if err == nil {
			return commitView(c), nil
		}
		r.dropNative() // perhaps in a pack written since it was opened (a fetch)
	}
	raw, err := r.clientGit(ctx, "", "log", "-1", "--no-show-signature", "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%s%x00%B", hash, "--")
	if err != nil {
		return GitCommit{}, errors.New("commit not found")
	}
	p := strings.SplitN(raw, "\x00", 7)
	if len(p) != 7 {
		return GitCommit{}, errors.New("commit not found")
	}
	return GitCommit{Hash: strings.TrimSpace(p[0]), Parents: strings.Fields(p[1]), Author: p[2], Email: p[3], Date: p[4], Subject: p[5], Message: p[6]}, nil
}

type GitCommitDetail struct {
	Commit GitCommit `json:"commit"`
	Files  []GitFile `json:"files"`
}

func (r *Repository) CommitDetail(ctx context.Context, hash string) (GitCommitDetail, error) {
	result := GitCommitDetail{Files: []GitFile{}}
	if !ValidCommit(hash) {
		return result, errors.New("a full commit hash is required")
	}
	commit, err := r.commitInfo(ctx, hash)
	if err != nil {
		return result, err
	}
	result.Commit = commit
	// The first parent, or the empty tree for a root commit: one git process in all, for the file list.
	parent := ""
	if len(commit.Parents) > 0 {
		parent = commit.Parents[0]
	} else if parent, err = r.commitParent(ctx, hash); err != nil {
		return result, err
	}
	raw, err := r.clientGit(ctx, "", "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", parent, hash, "--")
	if err != nil {
		return result, err
	}
	parts := strings.Split(raw, "\x00")
	for i := 0; i+1 < len(parts) && parts[i] != ""; {
		status := parts[i]
		file := GitFile{Index: status[:1], Path: parts[i+1]}
		i += 2
		if file.Index == "R" || file.Index == "C" {
			if i >= len(parts) {
				return result, errors.New("incomplete rename")
			}
			file.OldPath = file.Path
			file.Path = parts[i]
			i++
		}
		file.Protected = ValidPath(file.Path) != nil || (file.OldPath != "" && ValidPath(file.OldPath) != nil)
		result.Files = append(result.Files, file)
	}
	return result, nil
}

// GraphCommit is a commit with the branches and tags that point at it, for the history graph.
type GraphCommit struct {
	GitCommit
	Refs []string `json:"refs"`
}

// GraphPage is a page of history for the graph.
type GraphPage struct {
	Commits []GraphCommit `json:"commits"`
	Next    int           `json:"next"`
}

// Graph is the history of every branch (or just HEAD), newest first in topological order, so each commit
// comes before its parents and lines in the graph never cross back.
func (r *Repository) Graph(ctx context.Context, all bool, offset, limit int) (GraphPage, error) {
	page := GraphPage{Commits: []GraphCommit{}, Next: -1}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 500 {
		return page, errors.New("invalid history page")
	}
	// No --topo-order: it makes git walk the whole history before printing anything (a second on a
	// repository the size of Kubernetes, without a commit-graph file); in its own order git streams the
	// newest commits in milliseconds, children before parents unless clocks disagree, which the lane
	// layout tolerates.
	args := []string{"log", "--no-color", "--decorate=short", "--format=%H%x00%P%x00%an%x00%ae%x00%aI%x00%D%x00%s%x1e",
		"--skip=" + strconv.Itoa(offset), "-n", strconv.Itoa(limit + 1)}
	if all {
		args = append(args, "--branches", "--remotes", "--tags", "HEAD")
	} else {
		args = append(args, "HEAD")
	}
	out, err := r.clientGit(ctx, "", append(args, "--")...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") || strings.Contains(err.Error(), "unknown revision") || strings.Contains(err.Error(), "bad default revision") {
			return page, nil // nothing committed yet
		}
		return page, err
	}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		p := strings.Split(rec, "\x00")
		if len(p) != 7 {
			continue
		}
		if len(page.Commits) == limit {
			page.Next = offset + limit
			break
		}
		c := GraphCommit{GitCommit: GitCommit{Hash: p[0], Parents: strings.Fields(p[1]), Author: p[2], Email: p[3], Date: p[4], Subject: p[6], Message: p[6]}, Refs: []string{}}
		for _, ref := range strings.Split(p[5], ", ") {
			if ref = strings.TrimSpace(ref); ref != "" {
				c.Refs = append(c.Refs, ref)
			}
		}
		page.Commits = append(page.Commits, c)
	}
	return page, nil
}
