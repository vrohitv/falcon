package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/ide"
	"github.com/vrohitv/falcon/internal/repository"
	"github.com/vrohitv/falcon/internal/workspace"
)

func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	state, err := rp.Repo.GitState(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, state)
}

func (s *Server) gitLog(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 30
	}
	log, err := rp.Repo.CommitLog(r.Context(), r.URL.Query().Get("head"), offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, log)
}

func (s *Server) gitGraph(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 100
	}
	page, err := rp.Repo.Graph(r.Context(), r.URL.Query().Get("all") != "false", offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, page)
}

func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	detail, err := rp.Repo.CommitDetail(r.Context(), r.URL.Query().Get("commit"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, detail)
}

func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	path := r.URL.Query().Get("path")
	context := 3
	if c := r.URL.Query().Get("context"); c == "all" {
		context = 1000000
	} else if n, err := strconv.Atoi(c); err == nil {
		context = n
	}
	diff, truncated, err := rp.Repo.ClientDiffContext(r.Context(), path, r.URL.Query().Get("area"), r.URL.Query().Get("commit"), context)
	if err != nil {
		fail(w, err)
		return
	}
	hunks := ide.HighlightDiff(path, strings.TrimSuffix(diff, "\n"))
	if hunks == nil {
		hunks = []ide.DiffHunk{}
	}
	// Each hunk's ID lets the panel stage, unstage or discard it on its own.
	ids := []string{}
	if area := r.URL.Query().Get("area"); context == 3 && (area == "working" || area == "staged") {
		if got, err := rp.Repo.HunkIDs(r.Context(), path, area); err == nil && len(got) == len(hunks) {
			ids = got
		}
	}
	respond(w, map[string]any{"path": path, "hunks": hunks, "hunk_ids": ids, "binary": strings.Contains(diff, "Binary files ") || strings.Contains(diff, "GIT binary patch"), "truncated": truncated, "metadata_only": diff != "" && !strings.Contains(diff, "@@")})
}

// gitResult is what a Git action did.
type gitResult struct {
	ID      string                `json:"id"`
	Request repository.GitRequest `json:"request"`
	Status  string                `json:"status"` // succeeded, failed
	Result  string                `json:"result,omitempty"`
	Error   string                `json:"error,omitempty"`
	// State is the repository right after the action, so the panel needs no second request; Seq is the
	// change event the action caused, which that panel can then ignore.
	State *repository.GitState `json:"state,omitempty"`
	Seq   int                  `json:"seq,omitempty"`
}

// gitState remembers recent results by request ID, so a request sent twice (a retry after a dropped
// response) gets the first answer instead of running twice.
type gitState struct {
	mu   sync.Mutex
	done map[string]gitResult
	ids  []string
	// running holds the IDs in flight; a duplicate waits for the first.
	running map[string]chan struct{}
	watch   map[string]*gitWatch
}

func (s *Server) gitCommand(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	var req repository.GitRequest
	if err := body(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	if req.ID == "" || len(req.ID) > 160 {
		fail(w, fmt.Errorf("an operation ID of 1–160 characters is required"))
		return
	}
	key := rp.ID + "\x00" + req.ID
	g := &s.git
	for {
		g.mu.Lock()
		if g.done == nil {
			g.done, g.running = map[string]gitResult{}, map[string]chan struct{}{}
		}
		if res, ok := g.done[key]; ok {
			g.mu.Unlock()
			if string(domain.JSON(res.Request)) != string(domain.JSON(req)) {
				fail(w, fmt.Errorf("operation ID already belongs to another request"))
				return
			}
			respond(w, res)
			return
		}
		if wait, ok := g.running[key]; ok {
			g.mu.Unlock()
			<-wait
			continue
		}
		ch := make(chan struct{})
		g.running[key] = ch
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			delete(g.running, key)
			g.mu.Unlock()
			close(ch)
		}()
		break
	}
	limit := time.Minute
	if repository.NetworkAction(req.Action) {
		limit = 5 * time.Minute
	}
	// The action belongs to the server: a closed tab does not cut a push in half.
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	res := gitResult{ID: req.ID, Request: req, Status: "succeeded"}
	gw := s.currentWatch(rp)
	gw.quiet(limit)
	out, err := rp.Repo.ApplyGit(ctx, req)
	res.Result = out
	if err != nil {
		res.Status, res.Error = "failed", err.Error()
		if ctx.Err() != nil {
			res.Error = "Git took too long and was stopped; check the repository before retrying"
		}
	}
	// The state to answer with, and the outside-change watcher's new baseline (so it does not report this
	// change as someone else's), are read at the same time.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if st, e := rp.Repo.GitState(ctx); e == nil {
			res.State = &st
		}
	}()
	if gw != nil {
		gw.rebase(treePrint(rp))
	}
	wg.Wait()
	gw.quiet(400 * time.Millisecond) // Git's own writes are still arriving
	// The change's number is taken now and announced after the answer is sent, so the panel that asked
	// knows the event is its own before it arrives.
	s.index(rp).Invalidate()
	if gw != nil {
		res.Seq = gw.next()
	}
	g.mu.Lock()
	g.done[key] = res
	g.ids = append(g.ids, key)
	if len(g.ids) > 500 {
		delete(g.done, g.ids[0])
		g.ids = g.ids[1:]
	}
	g.mu.Unlock()
	respond(w, res)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if gw != nil {
		gw.announce(res.Seq)
	}
}

// gitWatch tells open Source Control panels that the repository changed: a commit, a checkout, staging
// from a terminal or another tool. It watches Git's own folder (HEAD, the index, refs), not the whole
// tree, so it costs nothing on a large repository; working-tree edits are seen when the window regains
// focus.
type gitWatch struct {
	mu   sync.Mutex
	subs map[chan int]struct{}
	n    int
	w    *fsnotify.Watcher
	t    *time.Timer
	// quietUntil: Falcon's own action is writing to .git; last/based: the working tree as last seen.
	quietUntil time.Time
	last       string
	gen        int
}

func (s *Server) currentWatch(rp *workspace.Repo) *gitWatch {
	s.git.mu.Lock()
	defer s.git.mu.Unlock()
	return s.git.watch[rp.ID]
}

// quiet ignores Git's own file events for a while (Falcon's action writes the index and refs; the panel
// that asked hears of it in the answer, the others from fire).
func (g *gitWatch) quiet(d time.Duration) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.quietUntil = time.Now().Add(d)
	g.mu.Unlock()
}

// rebase sets what the outside-change watcher compares against.
func (g *gitWatch) rebase(print string) {
	g.mu.Lock()
	g.last = print
	g.gen++
	g.mu.Unlock()
}

// next reserves the next change number; announce sends it.
func (g *gitWatch) next() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return g.n
}

func (g *gitWatch) announce(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for ch := range g.subs {
		select {
		case ch <- n:
		default:
		}
	}
}

func (g *gitWatch) fire() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	for ch := range g.subs {
		select {
		case ch <- g.n:
		default:
		}
	}
	return g.n
}

func (s *Server) watchGit(rp *workspace.Repo) (*gitWatch, chan int, error) {
	s.git.mu.Lock()
	defer s.git.mu.Unlock()
	if s.git.watch == nil {
		s.git.watch = map[string]*gitWatch{}
	}
	gw := s.git.watch[rp.ID]
	if gw == nil {
		dir, err := rp.Repo.Git(context.Background(), "rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, nil, err
		}
		dir = strings.TrimSpace(dir)
		fw, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, nil, err
		}
		gw = &gitWatch{subs: map[chan int]struct{}{}, w: fw}
		_ = fw.Add(dir)
		for _, sub := range []string{"refs", "refs/heads", "refs/remotes", "refs/tags"} {
			_ = filepath.WalkDir(filepath.Join(dir, sub), func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					_ = fw.Add(p)
				}
				return nil
			})
		}
		go func() {
			for {
				select {
				case e, ok := <-fw.Events:
					if !ok {
						return
					}
					// Only content changes count: reading a file (git reads the index on every command)
					// reports an attribute change on macOS, and reacting to it would loop forever.
					if e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
						continue
					}
					base := filepath.Base(e.Name)
					if strings.HasSuffix(base, ".lock") || base == "FETCH_HEAD" || strings.HasPrefix(base, "tmp") {
						continue
					}
					if e.Op&fsnotify.Create != 0 {
						if info, err := os.Stat(e.Name); err == nil && info.IsDir() {
							_ = fw.Add(e.Name)
						}
					}
					// Git writes several files per operation: one notification after it settles.
					gw.mu.Lock()
					if time.Now().Before(gw.quietUntil) {
						gw.mu.Unlock()
						continue
					}
					if gw.t != nil {
						gw.t.Stop()
					}
					gw.t = time.AfterFunc(120*time.Millisecond, func() {
						gw.fire()
						// A commit, a checkout, a pull: what changed is reported to the language servers.
						go s.reportChanges(rp)
					})
					gw.mu.Unlock()
				case _, ok := <-fw.Errors:
					if !ok {
						return
					}
				}
			}
		}()
		s.git.watch[rp.ID] = gw
		// The first look at the working tree is taken now, before anyone is told the watch exists.
		gw.rebase(treePrint(rp))
		go s.watchTree(rp, gw)
	}
	ch := make(chan int, 1)
	gw.mu.Lock()
	gw.subs[ch] = struct{}{}
	gw.mu.Unlock()
	return gw, ch, nil
}

// treePrint is the working tree as git status sees it, with each changed file's size and time.
func treePrint(rp *workspace.Repo) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := rp.Repo.Git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(out)
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) > 3 {
			if info, err := os.Lstat(filepath.Join(rp.Path, rec[3:])); err == nil {
				fmt.Fprintf(&b, "|%d.%d", info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	return b.String()
}

// watchTree notices working-tree edits made outside Falcon (an editor, a terminal, a build) while someone
// is watching, and tells them. The folder's watcher says when something changed; only then is git
// status run (at most once per pause), so an idle folder costs nothing. Without a watcher (the system
// cannot watch the tree) it is checked every pause instead. It stops when nobody is subscribed.
func (s *Server) watchTree(rp *workspace.Repo, gw *gitWatch) {
	// The pause adapts: at least 1.5 s apart, and four times as long as the last status took, so a large
	// repository (or a slow disk, or Windows with an antivirus) never spends more than a fifth of its
	// time on this.
	pause := func(took time.Duration) time.Duration { return max(1500*time.Millisecond, 4*took) }
	x := s.index(rp)
	x.Watch()
	changes, unsubscribe := x.Changes()
	defer func() { unsubscribe() }()
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	var later <-chan time.Time
	due := false
	// The baseline was taken by watchGit, before the watch was handed out.
	next := time.Now().Add(pause(0))
	for {
		select {
		case _, ok := <-changes:
			if !ok {
				changes = nil // the index closed: check every pause from now on
				continue
			}
			due = true
		case <-tick.C:
			// Under the workspace lock, so a subscriber arriving now gets a fresh watch instead of this one.
			s.git.mu.Lock()
			gw.mu.Lock()
			idle := len(gw.subs) == 0
			gw.mu.Unlock()
			if idle && s.git.watch[rp.ID] == gw {
				delete(s.git.watch, rp.ID)
			}
			s.git.mu.Unlock()
			if idle {
				gw.w.Close()
				return
			}
			if changes != nil {
				s.touchIndex(rp) // someone is looking: the folder's index (and its watcher) stays
			}
			if changes == nil || !x.Live() {
				due = true
			}
		case <-later:
			later = nil
		}
		if !due {
			continue
		}
		if wait := time.Until(next); wait > 0 {
			if later == nil {
				later = time.After(wait)
			}
			continue
		}
		due = false
		gw.mu.Lock()
		gen := gw.gen
		gw.mu.Unlock()
		started := time.Now()
		fp := treePrint(rp)
		next = time.Now().Add(pause(time.Since(started)))
		if fp == "" {
			continue
		}
		gw.mu.Lock()
		// A baseline set meanwhile (Falcon's own action) is newer than this look: keep it.
		changed := gen == gw.gen && fp != gw.last
		if gen == gw.gen {
			gw.last = fp
		}
		gw.mu.Unlock()
		if changed {
			s.index(rp).Invalidate()
			gw.fire()
			go s.reportChanges(rp)
		}
	}
}
