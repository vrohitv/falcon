package term

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// backlogBytes of a session's latest output are kept, so a page that left (a reload, another tab)
	// comes back to what was on screen.
	backlogBytes = 1 << 20
	// A viewer may fall this far behind (sent but not yet drawn) before the shell is paused.
	viewerWindow = 4 << 20
	// sessionsPerFolder bounds what one folder can start.
	sessionsPerFolder = 32
)

// Info describes a session.
type Info struct {
	ID      string    `json:"id"`
	Folder  string    `json:"-"`
	Title   string    `json:"title"`
	Shell   string    `json:"shell"`
	Created time.Time `json:"created"`
	Cols    int       `json:"cols"`
	Rows    int       `json:"rows"`
}

// Session is a shell on a pseudo-terminal. It outlives the pages that show it: they attach and detach.
type Session struct {
	Info
	pty     PTY
	mu      sync.Mutex
	backlog []byte
	viewers map[*Viewer]struct{}
	done    chan struct{}
	code    int
	onExit  func(*Session)
}

// Viewer is one page showing a session: output queues for it, and the shell waits while it is more than
// viewerWindow behind (it confirms what it has drawn with Ack).
type Viewer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    bytes.Buffer
	inflight int
	closed   bool
}

func newViewer() *Viewer {
	v := &Viewer{}
	v.cond = sync.NewCond(&v.mu)
	return v
}

// push queues output, waiting while the viewer is too far behind.
func (v *Viewer) push(b []byte) {
	v.mu.Lock()
	for !v.closed && v.inflight+v.queue.Len() > viewerWindow {
		v.cond.Wait()
	}
	if !v.closed {
		v.queue.Write(b)
		v.cond.Broadcast()
	}
	v.mu.Unlock()
}

// Next is the output queued since the last call (at most max bytes, coalesced into one piece), waiting
// for some; it is nil once the viewer is closed.
func (v *Viewer) Next(max int) []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	for !v.closed && v.queue.Len() == 0 {
		v.cond.Wait()
	}
	if v.closed {
		return nil
	}
	n := min(max, v.queue.Len())
	out := make([]byte, n)
	copy(out, v.queue.Next(n))
	v.inflight += n
	return out
}

// Ack confirms n bytes were drawn.
func (v *Viewer) Ack(n int) {
	v.mu.Lock()
	v.inflight = max(0, v.inflight-n)
	v.cond.Broadcast()
	v.mu.Unlock()
}

// Close stops the viewer (its Next returns nil; the shell no longer waits for it).
func (v *Viewer) Close() {
	v.mu.Lock()
	v.closed = true
	v.cond.Broadcast()
	v.mu.Unlock()
}

// Attach adds a viewer, which first receives the backlog (what is on screen), then what follows.
func (s *Session) Attach() *Viewer {
	v := newViewer()
	s.mu.Lock()
	v.queue.Write(s.backlog)
	s.viewers[v] = struct{}{}
	s.mu.Unlock()
	return v
}

// Detach removes a viewer; the session goes on.
func (s *Session) Detach(v *Viewer) {
	s.mu.Lock()
	delete(s.viewers, v)
	s.mu.Unlock()
	v.Close()
}

// Write types into the shell.
func (s *Session) Write(b []byte) error {
	select {
	case <-s.done:
		return errors.New("the shell has exited")
	default:
	}
	_, err := s.pty.Write(b)
	return err
}

// Resize sets the terminal's size.
func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	s.Cols, s.Rows = clamp(cols, 2, 1000), clamp(rows, 1, 1000)
	s.mu.Unlock()
	return s.pty.Resize(cols, rows)
}

// Kill ends the shell and everything it started.
func (s *Session) Kill() { s.pty.Kill() }

// Done is closed when the shell has exited; Code is then its exit code.
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Code() int             { return s.code }

// pump reads the shell's output: it is kept (the backlog) and handed to every viewer.
func (s *Session) pump() {
	buf := make([]byte, 64<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.backlog = append(s.backlog, chunk...)
			if len(s.backlog) > backlogBytes {
				// Cut at a line break, so the kept output does not start inside an escape sequence.
				cut := len(s.backlog) - backlogBytes
				if i := bytes.IndexByte(s.backlog[cut:], '\n'); i >= 0 && i < 4096 {
					cut += i + 1
				}
				s.backlog = append([]byte(nil), s.backlog[cut:]...)
			}
			viewers := make([]*Viewer, 0, len(s.viewers))
			for v := range s.viewers {
				viewers = append(viewers, v)
			}
			s.mu.Unlock()
			for _, v := range viewers {
				v.push(chunk)
			}
		}
		if err != nil {
			break
		}
	}
	code, _ := s.pty.Wait()
	s.pty.Kill()
	s.code = code
	close(s.done)
	s.mu.Lock()
	for v := range s.viewers {
		v.Close()
	}
	s.mu.Unlock()
	if s.onExit != nil {
		s.onExit(s)
	}
}

// Manager keeps every folder's sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// OnExit is told when a shell exits (after its last output reached its viewers).
	OnExit func(Info, int)
}

func NewManager() *Manager { return &Manager{sessions: map[string]*Session{}} }

// Open starts the default shell in a folder.
func (m *Manager) Open(folder string, cols, rows int) (*Session, error) {
	m.mu.Lock()
	n := 0
	for _, s := range m.sessions {
		if s.Folder == folder {
			n++
		}
	}
	m.mu.Unlock()
	if n >= sessionsPerFolder {
		return nil, errors.New("too many terminals open in this folder; close one first")
	}
	argv := DefaultShell()
	p, err := Start(argv, folder, Environment(), cols, rows)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	shell := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	s := &Session{
		Info:    Info{ID: hex.EncodeToString(b), Folder: folder, Title: shell, Shell: argv[0], Created: time.Now().UTC(), Cols: clamp(cols, 2, 1000), Rows: clamp(rows, 1, 1000)},
		pty:     p,
		viewers: map[*Viewer]struct{}{},
		done:    make(chan struct{}),
	}
	s.onExit = func(s *Session) {
		m.mu.Lock()
		delete(m.sessions, s.ID)
		m.mu.Unlock()
		if m.OnExit != nil {
			m.OnExit(s.Info, s.code)
		}
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	go s.pump()
	return s, nil
}

// Get is a session of a folder (nil when it is not, or has exited).
func (m *Manager) Get(folder, id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil && s.Folder == folder {
		return s
	}
	return nil
}

// List is a folder's sessions, oldest first.
func (m *Manager) List(folder string) []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Info{}
	for _, s := range m.sessions {
		if s.Folder == folder {
			s.mu.Lock()
			out = append(out, s.Info)
			s.mu.Unlock()
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// CloseFolder ends a folder's sessions (it is no longer served).
func (m *Manager) CloseFolder(folder string) {
	m.mu.Lock()
	var doomed []*Session
	for _, s := range m.sessions {
		if s.Folder == folder {
			doomed = append(doomed, s)
		}
	}
	m.mu.Unlock()
	for _, s := range doomed {
		s.Kill()
	}
}

// Close ends every session (Falcon is stopping).
func (m *Manager) Close() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Kill()
	}
}

// DefaultShell is the shell a terminal runs: the owner's ($SHELL) on Linux; PowerShell 7, Windows
// PowerShell or cmd on Windows.
func DefaultShell() []string {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return []string{p, "-NoLogo"}
			}
		}
		if c := os.Getenv("COMSPEC"); c != "" {
			return []string{c}
		}
		return []string{"cmd.exe"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh}
		}
	}
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return []string{sh}
		}
	}
	return []string{"sh"}
}

// Environment is a terminal's environment: the owner's own (it is their shell), without Falcon's access
// token, and with what terminal programs look for.
func Environment() []string {
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "FALCON_TOKEN", "TERM", "COLORTERM", "TERM_PROGRAM":
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=Falcon")
}
