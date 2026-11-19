package lsp

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pool keeps language servers running between requests. A server like gopls, tsserver, rust-analyzer or
// jdtls spends seconds to minutes reading a project before it can answer "who calls this?"; starting one
// per click, as before, meant waiting for that every time and often answering from half a picture. Here a
// server starts on first use, is told about each document as it changes, and stops after it has been idle
// a while (or when Falcon stops).
type Pool struct {
	// Idle is how long a server may go unused before it is stopped.
	Idle    time.Duration
	mu      sync.Mutex
	servers map[string]*Running
	stop    chan struct{}
	once    sync.Once
}

// Running is one language server in the pool.
type Running struct {
	Key     string
	Root    string
	Command string
	Started time.Time
	client  *Client
	mu      sync.Mutex
	used    time.Time
	sent    map[string][32]byte
	ready   chan struct{}
	err     error
}

// Client is the running server's connection (nil until it has started).
func (r *Running) Client() *Client {
	select {
	case <-r.ready:
		return r.client
	default:
		return nil
	}
}

// NewPool makes an empty pool and starts its reaper.
func NewPool(idle time.Duration) *Pool {
	p := &Pool{Idle: idle, servers: map[string]*Running{}, stop: make(chan struct{})}
	go p.reap()
	return p
}

// Get gives the running server for `o` (by command, arguments and root), starting it if it is not.
// Concurrent callers wait for the one start.
func (p *Pool) Get(ctx context.Context, o Options) (*Running, error) {
	key := o.Root + "\x00" + o.Command + "\x00" + strings.Join(o.Args, "\x00")
	p.mu.Lock()
	r := p.servers[key]
	if r != nil {
		select {
		case <-r.ready:
			// One that failed or exited is started again.
			if r.err != nil || r.client == nil || !r.client.Alive() {
				r = nil
			}
		default:
		}
	}
	if r == nil {
		r = &Running{Key: key, Root: o.Root, Command: o.Command, Started: time.Now(), used: time.Now(), sent: map[string][32]byte{}, ready: make(chan struct{})}
		p.servers[key] = r
		go func() {
			// The start belongs to the pool, not to the request that happened to cause it.
			r.client, r.err = StartWith(context.Background(), o)
			close(r.ready)
		}()
	}
	p.mu.Unlock()
	select {
	case <-r.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.err != nil {
		p.mu.Lock()
		if p.servers[key] == r {
			delete(p.servers, key)
		}
		p.mu.Unlock()
		return nil, r.err
	}
	r.mu.Lock()
	r.used = time.Now()
	r.mu.Unlock()
	return r, nil
}

// Sync tells the server what a document holds now: opens it the first time, sends its new text when it
// changed. Unchanged text sends nothing.
func (r *Running) Sync(uri, language, text string) error {
	sum := sha256.Sum256([]byte(text))
	r.mu.Lock()
	same := r.sent[uri] == sum
	r.sent[uri] = sum
	r.mu.Unlock()
	if same {
		return nil
	}
	return r.client.Sync(uri, language, text)
}

// Status is one server's state, for the editor's pill.
type Status struct {
	Root    string    `json:"root"`
	Command string    `json:"command"`
	State   string    `json:"state"`
	Doing   string    `json:"doing,omitempty"`
	Started time.Time `json:"started"`
	Docs    int       `json:"documents"`
}

// Statuses lists the running servers.
func (p *Pool) Statuses() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Status{}
	for _, r := range p.servers {
		s := Status{Root: r.Root, Command: r.Command, Started: r.Started}
		select {
		case <-r.ready:
			switch {
			case r.err != nil:
				s.State, s.Doing = "failed", r.err.Error()
			case !r.client.Alive():
				s.State = "exited"
			default:
				s.State = "ready"
				if busy, what := r.client.Busy(); busy {
					s.State, s.Doing = "indexing", what
				}
			}
		default:
			s.State = "starting"
		}
		r.mu.Lock()
		s.Docs = len(r.sent)
		r.mu.Unlock()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// Close stops every server.
func (p *Pool) Close() {
	p.once.Do(func() { close(p.stop) })
	p.mu.Lock()
	all := p.servers
	p.servers = map[string]*Running{}
	p.mu.Unlock()
	for _, r := range all {
		go func(r *Running) {
			<-r.ready
			if r.client != nil {
				r.client.Close()
			}
		}(r)
	}
}

func (p *Pool) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		for key, r := range p.servers {
			r.mu.Lock()
			idle := time.Since(r.used)
			r.mu.Unlock()
			if idle > p.Idle && r.Client() != nil {
				delete(p.servers, key)
				go r.client.Close()
			}
		}
		p.mu.Unlock()
	}
}

// passed are the variables a language server needs from the owner's environment: where their tools and
// caches are (so gopls reads the module cache instead of downloading, rust-analyzer finds the toolchain,
// jdtls finds Java), and nothing else (no API keys or tokens).
var passed = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TMPDIR", "TERM",
	"TEMP", "TMP", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH", "PROGRAMDATA", "ProgramFiles", "ProgramFiles(x86)",
	"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE", "GOFLAGS", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "GOTOOLCHAIN", "GOWORK",
	"CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN", "CARGO_TARGET_DIR",
	"JAVA_HOME", "JDK_HOME", "MAVEN_HOME", "M2_HOME", "GRADLE_HOME", "GRADLE_USER_HOME",
	"NODE_PATH", "NVM_DIR", "NPM_CONFIG_PREFIX",
	"VIRTUAL_ENV", "CONDA_PREFIX", "PYTHONPATH", "PYENV_ROOT", "UV_CACHE_DIR",
	"SDKROOT", "DEVELOPER_DIR", "CC", "CXX", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}

// Env is the environment a language server runs with: the passed variables, plus `extra` (NAME=value).
func Env(extra ...string) []string {
	out := []string{}
	for _, k := range passed {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return append(out, extra...)
}

// ErrNoServer reports a language with no installed server.
var ErrNoServer = errors.New("no language server is installed for this language")

// CloseRoot stops the servers running for one folder (it was removed).
func (p *Pool) CloseRoot(root string) {
	p.mu.Lock()
	var stop []*Running
	for key, r := range p.servers {
		if strings.HasPrefix(key, root+"\x00") {
			stop = append(stop, r)
			delete(p.servers, key)
		}
	}
	p.mu.Unlock()
	for _, r := range stop {
		go func(r *Running) {
			<-r.ready
			if r.client != nil {
				r.client.Close()
			}
		}(r)
	}
}
