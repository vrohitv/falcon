package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/vrohitv/falcon/internal/term"
	"github.com/vrohitv/falcon/internal/workspace"
)

// One WebSocket per page carries everything the server pushes: git changes, changes to open files, and
// terminals. (Server-sent events, one connection each, ran into the browser's six connections per host:
// a third Falcon tab stalled every request. WebSockets do not count against that limit.) Requests stay
// plain HTTP, which keeps compression, caching and cancellation.
//
// Text messages are JSON with a "t" (type). Binary messages are terminal data: one byte giving the
// length of the session ID, the ID, then the bytes (typed input from the page, output from the shell).
//
// page → server: {"t":"watch","paths":[…]} replaces the files watched; {"t":"term.open","req":…,
// "cols":…,"rows":…}; {"t":"term.attach","id":…}; {"t":"term.detach","id":…}; {"t":"term.resize",
// "id":…,"cols":…,"rows":…}; {"t":"term.close","id":…}; {"t":"term.ack","id":…,"bytes":…} (output drawn).
//
// server → page: {"t":"hello","terminal":bool}; {"t":"git","n":…}; {"t":"file",…};
// {"t":"watch.unavailable"}; {"t":"term.opened","req":…,"session":…}; {"t":"term.attached","id":…};
// {"t":"term.gone","id":…}; {"t":"term.exit","id":…,"code":…}; {"t":"error","req":…,"message":…}.
type wsIn struct {
	T     string   `json:"t"`
	Req   string   `json:"req"`
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
	Cols  int      `json:"cols"`
	Rows  int      `json:"rows"`
	Bytes int      `json:"bytes"`
}

type wsConn struct {
	s      *Server
	rp     *workspace.Repo
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	files  *watchSub
	views  map[string]*term.Viewer
	wg     sync.WaitGroup
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	// The origin was checked by the middleware (only Falcon's own pages may connect), and the token too.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	conn := &wsConn{s: s, rp: rp, c: c, ctx: ctx, cancel: cancel, views: map[string]*term.Viewer{}}
	defer conn.close()

	// Git changes. The watch (and its first look at the working tree) starts before "hello": the page
	// refreshes on hello, so a change made before the first look is in that refresh, and one made after
	// it is an event. Nothing falls between.
	gw, ch, err := s.watchGit(rp)
	conn.send(map[string]any{"t": "hello", "terminal": s.Terminal})
	if err == nil {
		conn.wg.Add(1)
		go func() {
			defer conn.wg.Done()
			defer func() {
				gw.mu.Lock()
				delete(gw.subs, ch)
				gw.mu.Unlock()
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case n := <-ch:
					conn.send(map[string]any{"t": "git", "n": n})
				}
			}
		}()
	}
	// A ping now and then finds a connection that went away without closing.
	conn.wg.Add(1)
	go func() {
		defer conn.wg.Done()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if kind == websocket.MessageBinary {
			conn.input(data)
			continue
		}
		var in wsIn
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		conn.handle(in)
	}
}

func (c *wsConn) send(v any) {
	b, _ := json.Marshal(v)
	wctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	if err := c.c.Write(wctx, websocket.MessageText, b); err != nil {
		c.cancel()
	}
}

func (c *wsConn) handle(in wsIn) {
	switch in.T {
	case "watch":
		c.watch(in.Paths)
	case "term.open":
		if !c.s.Terminal {
			c.send(map[string]any{"t": "error", "req": in.Req, "message": "terminals are turned off (Falcon was started with -terminal=false)"})
			return
		}
		sess, err := c.s.terms.Open(c.rp.Repo.Path, in.Cols, in.Rows)
		if err != nil {
			c.send(map[string]any{"t": "error", "req": in.Req, "message": err.Error()})
			return
		}
		c.send(map[string]any{"t": "term.opened", "req": in.Req, "session": sess.Info})
		c.attach(sess)
	case "term.attach":
		if sess := c.s.terms.Get(c.rp.Repo.Path, in.ID); sess != nil {
			if in.Cols > 0 && in.Rows > 0 {
				_ = sess.Resize(in.Cols, in.Rows)
			}
			c.attach(sess)
		} else {
			c.send(map[string]any{"t": "term.gone", "id": in.ID})
		}
	case "term.detach":
		c.detach(in.ID)
	case "term.resize":
		if sess := c.s.terms.Get(c.rp.Repo.Path, in.ID); sess != nil {
			_ = sess.Resize(in.Cols, in.Rows)
		}
	case "term.close":
		if sess := c.s.terms.Get(c.rp.Repo.Path, in.ID); sess != nil {
			sess.Kill()
		}
	case "term.ack":
		c.mu.Lock()
		v := c.views[in.ID]
		c.mu.Unlock()
		if v != nil {
			v.Ack(in.Bytes)
		}
	}
}

// watch replaces the files whose changes are pushed.
func (c *wsConn) watch(paths []string) {
	c.mu.Lock()
	old := c.files
	c.files = nil
	c.mu.Unlock()
	if old != nil {
		c.s.watcher.unsubscribe(old)
	}
	if len(paths) == 0 {
		return
	}
	if len(paths) > maxWatched {
		paths = paths[:maxWatched]
	}
	files := map[string]string{}
	for _, p := range paths {
		if abs, err := c.s.readable(c.rp, p); err == nil {
			files[abs] = p
		}
	}
	sub, err := c.s.watcher.subscribe(files)
	if err != nil {
		c.send(map[string]any{"t": "watch.unavailable", "message": err.Error()})
		return
	}
	c.mu.Lock()
	c.files = sub
	c.mu.Unlock()
	for abs, rel := range files {
		hash, exists := currentHash(abs)
		c.send(map[string]any{"t": "file", "path": rel, "hash": hash, "exists": exists})
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-sub.done:
				return // replaced by a newer set of files
			case e, ok := <-sub.ch:
				if !ok {
					return
				}
				c.send(map[string]any{"t": "file", "path": e.Path, "hash": e.Hash, "exists": e.Exists})
			}
		}
	}()
}

// attach shows a session on this page: what is on its screen, then what follows, as binary frames
// coalesced up to 256 KB (fewer, larger frames when a program prints a lot).
func (c *wsConn) attach(sess *term.Session) {
	c.mu.Lock()
	if _, ok := c.views[sess.ID]; ok {
		c.mu.Unlock()
		return
	}
	v := sess.Attach()
	c.views[sess.ID] = v
	c.mu.Unlock()
	c.send(map[string]any{"t": "term.attached", "id": sess.ID})
	head := append([]byte{byte(len(sess.ID))}, sess.ID...)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			b := v.Next(256 << 10)
			if b == nil {
				break
			}
			frame := make([]byte, 0, len(head)+len(b))
			frame = append(append(frame, head...), b...)
			wctx, cancel := context.WithTimeout(c.ctx, 60*time.Second)
			err := c.c.Write(wctx, websocket.MessageBinary, frame)
			cancel()
			if err != nil {
				c.cancel()
				break
			}
		}
		select {
		case <-sess.Done():
			c.send(map[string]any{"t": "term.exit", "id": sess.ID, "code": sess.Code()})
		default:
		}
		c.mu.Lock()
		if c.views[sess.ID] == v {
			delete(c.views, sess.ID)
		}
		c.mu.Unlock()
	}()
}

func (c *wsConn) detach(id string) {
	c.mu.Lock()
	v := c.views[id]
	delete(c.views, id)
	c.mu.Unlock()
	if v != nil {
		if sess := c.s.terms.Get(c.rp.Repo.Path, id); sess != nil {
			sess.Detach(v)
		} else {
			v.Close()
		}
	}
}

// input is typed text for a session.
func (c *wsConn) input(data []byte) {
	if len(data) < 2 || int(data[0])+1 > len(data) {
		return
	}
	id := string(data[1 : 1+int(data[0])])
	c.mu.Lock()
	_, attached := c.views[id]
	c.mu.Unlock()
	if !attached {
		return // only a session this page shows takes its typing
	}
	if sess := c.s.terms.Get(c.rp.Repo.Path, id); sess != nil {
		_ = sess.Write(data[1+int(data[0]):])
	}
}

// close ends the page's connection: its sessions go on (a reload comes back to them).
func (c *wsConn) close() {
	c.cancel()
	c.mu.Lock()
	ids := make([]string, 0, len(c.views))
	for id := range c.views {
		ids = append(ids, id)
	}
	files := c.files
	c.files = nil
	c.mu.Unlock()
	for _, id := range ids {
		c.detach(id)
	}
	if files != nil {
		c.s.watcher.unsubscribe(files)
	}
	_ = c.c.Close(websocket.StatusNormalClosure, "")
	c.wg.Wait()
}

// terminals lists a folder's running terminals (a page shows them again after a reload).
func (s *Server) terminals(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	if !s.Terminal {
		respond(w, map[string]any{"enabled": false, "sessions": []term.Info{}})
		return
	}
	respond(w, map[string]any{"enabled": true, "sessions": s.terms.List(rp.Repo.Path)})
}
