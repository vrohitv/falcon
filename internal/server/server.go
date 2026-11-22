// Package server is Falcon's HTTP API and web UI. Every folder has its own API under /api/r/{repo}/; the
// workspace (adding, removing, browsing folders) is under /api/repos. Nothing here calls the network.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vrohitv/falcon/internal/domain"
	"github.com/vrohitv/falcon/internal/index"
	"github.com/vrohitv/falcon/internal/lsp"
	"github.com/vrohitv/falcon/internal/term"
	"github.com/vrohitv/falcon/internal/workspace"
	webassets "github.com/vrohitv/falcon/web"
)

// Server serves a workspace.
type Server struct {
	Workspace *workspace.Workspace
	// Token, when set, is required on every API request (needed to listen beyond loopback).
	Token string
	// Assets serves the UI from a folder instead of the embedded build (development).
	Assets string
	// Data is where Falcon keeps its own state (jdtls's index, the folder list).
	Data string
	// NoLSP turns language servers off.
	NoLSP bool
	// Terminal allows terminals (a shell in the folder, as the owner). On by default on loopback; served
	// beyond this machine it must be asked for (-terminal), since the token then gives a shell.
	Terminal  bool
	terms     *term.Manager
	Handler   http.Handler
	watcher   fileWatcher
	lsps      *lsp.Pool
	lspRuns   lspRuns
	external  externalFiles
	lspWatch  lspWatch
	edits     pendingEdits
	socks     sockets
	git       gitState
	idxMu     sync.Mutex
	indexes   map[string]*index.Index
	indexUsed map[string]time.Time
	// searches remembers each folder's last search, so typing narrows instead of starting over.
	searches map[string]search
}

// index is a folder's file, symbol and text index, made (and loaded from disk) on first use. An index
// unused for a while is let go (the symbol index stays on disk), so a server with many folders only holds
// the ones being read.
func (s *Server) index(rp *workspace.Repo) *index.Index {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	if s.indexes == nil {
		s.indexes = map[string]*index.Index{}
		s.indexUsed = map[string]time.Time{}
		go s.sweepIndexes()
	}
	x := s.indexes[rp.ID]
	if x == nil {
		x = index.New(rp.Repo, s.indexFile(rp.ID))
		s.indexes[rp.ID] = x
	}
	s.indexUsed[rp.ID] = time.Now()
	return x
}

// touchIndex marks a folder's index as in use, if it exists.
func (s *Server) touchIndex(rp *workspace.Repo) {
	s.idxMu.Lock()
	if _, ok := s.indexes[rp.ID]; ok {
		s.indexUsed[rp.ID] = time.Now()
	}
	s.idxMu.Unlock()
}

func (s *Server) indexFile(id string) string {
	if s.Data == "" {
		return ""
	}
	return filepath.Join(s.Data, "index", id+".gob")
}

// IndexIdle is how long an unused folder's index stays in memory.
var IndexIdle = 30 * time.Minute

func (s *Server) sweepIndexes() {
	for {
		time.Sleep(time.Minute)
		s.idxMu.Lock()
		for id, at := range s.indexUsed {
			if time.Since(at) > IndexIdle {
				if x := s.indexes[id]; x != nil {
					x.Close() // its watcher stops too
				}
				delete(s.indexes, id)
				delete(s.indexUsed, id)
				delete(s.searches, id)
			}
		}
		s.idxMu.Unlock()
	}
}

// forget drops everything kept for a folder that is no longer served: its index (and the copy on disk),
// its last search, its language servers.
func (s *Server) forget(rp *workspace.Repo) {
	s.terms.CloseFolder(rp.Repo.Path)
	s.idxMu.Lock()
	if x := s.indexes[rp.ID]; x != nil {
		x.Close()
	}
	delete(s.indexes, rp.ID)
	delete(s.indexUsed, rp.ID)
	delete(s.searches, rp.ID)
	s.idxMu.Unlock()
	if f := s.indexFile(rp.ID); f != "" {
		_ = os.Remove(f)
		_ = os.Remove(strings.TrimSuffix(f, ".gob") + ".text.gob")
	}
	s.lsps.CloseRoot(rp.Repo.Path)
}

type repoHandler func(w http.ResponseWriter, r *http.Request, rp *workspace.Repo)

// Version is stamped at release build time.
var Version = "0.1.0"

// New builds the server and its routes.
func New(ws *workspace.Workspace, token, assets, data string) *Server {
	s := &Server{Workspace: ws, Token: token, Assets: assets, Data: data, lsps: lsp.NewPool(20 * time.Minute), terms: term.NewManager()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/repos", s.repos)
	mux.HandleFunc("POST /api/repos", s.addRepo)
	mux.HandleFunc("DELETE /api/repos/{repo}", s.removeRepo)
	mux.HandleFunc("GET /api/browse", s.browse)
	repo := map[string]repoHandler{
		"GET tree":             s.directory,
		"GET files":            s.files,
		"GET fuzzy":            s.fuzzy,
		"GET code":             s.codeWindow,
		"GET code/shape":       s.codeShape,
		"POST highlight":       s.highlight,
		"GET outline":          s.outline,
		"GET definition":       s.definitions,
		"GET symbols":          s.symbols,
		"GET file":             s.file,
		"PUT file":             s.saveFile,
		"GET file/stat":        s.fileStat,
		"GET raw":              s.raw,
		"GET search":           s.search,
		"GET summary":          s.summary,
		"GET languages":        s.languages,
		"GET lsp/status":       s.lspStatus,
		"POST lsp":             s.lsp,
		"POST lsp/diagnostics": s.lspDiagnostics,
		"POST lsp/apply":       s.lspApply,
		"GET lsp/problems":     s.lspProblems,
		"GET git/status":       s.gitStatus,
		"GET git/log":          s.gitLog,
		"GET git/graph":        s.gitGraph,
		"GET git/commit":       s.gitCommit,
		"GET git/diff":         s.gitDiff,
		"POST git/commands":    s.gitCommand,
		"GET ws":               s.socket,
		"GET terminals":        s.terminals,
	}
	for route, h := range repo {
		method, rest, _ := strings.Cut(route, " ")
		mux.HandleFunc(method+" /api/r/{repo}/"+rest, s.with(h))
	}
	mux.HandleFunc("/", s.static)
	s.Handler = s.middleware(mux)
	return s
}

// with resolves the folder named in the URL.
func (s *Server) with(h repoHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rp, ok := s.Workspace.Get(r.PathValue("repo"))
		if !ok {
			fail(w, errors.Join(domain.ErrNotFound, errors.New("this folder is not open in Falcon")))
			return
		}
		h(w, r, rp)
	}
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, domain.ErrConflict) {
		code = http.StatusConflict
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		code = http.StatusNotFound
	}
	msg := err.Error()
	if errors.Is(err, domain.ErrNotFound) && strings.HasPrefix(msg, domain.ErrNotFound.Error()+"\n") {
		msg = strings.TrimPrefix(msg, domain.ErrNotFound.Error()+"\n")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func body(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("http panic", "error", v)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasSuffix(r.URL.Path, "/raw") {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		} else {
			w.Header().Set("X-Frame-Options", "DENY")
		}
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		// Without a token only loopback names are served (no DNS rebinding from a web page).
		if s.Token == "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "untrusted host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				http.Error(w, "cross-origin request denied", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if acceptsGzip(r) && !strings.HasSuffix(r.URL.Path, "/ws") {
				w = &gzipJSON{ResponseWriter: w}
			}
			if r.URL.Path != "/api/login" && s.Token != "" {
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if cookie, err := r.Cookie("falcon_session"); err == nil && token == "" {
					token = cookie.Value
				}
				if subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(401)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Authentication required"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := body(w, r, &input); err != nil {
		fail(w, err)
		return
	}
	if s.Token == "" || subtle.ConstantTimeCompare([]byte(input.Token), []byte(s.Token)) != 1 {
		w.WriteHeader(401)
		respond(w, map[string]string{"error": "wrong token"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "falcon_session", Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	respond(w, map[string]bool{"ok": true})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	var assets fs.FS
	if s.Assets != "" {
		assets = os.DirFS(s.Assets)
	} else {
		assets, _ = fs.Sub(webassets.Dist, "dist")
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		// /r/<id>/… and other app routes are the app.
		if strings.HasPrefix(name, "assets/") || (strings.Contains(path.Base(name), ".") && !strings.HasPrefix(name, "r/")) {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		data, err = fs.ReadFile(assets, name)
	}
	if err != nil {
		http.Error(w, "Frontend not built. Run: cd web && npm ci && npm run build", http.StatusServiceUnavailable)
		return
	}
	switch path.Ext(name) {
	case ".js":
		w.Header().Set("Content-Type", "text/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".woff2":
		w.Header().Set("Content-Type", "font/woff2")
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		// Nothing loads from anywhere but Falcon itself.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self'; font-src 'self'; connect-src 'self'; frame-src 'self'; object-src 'self'; frame-ancestors 'none'")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	switch path.Ext(name) {
	case ".js", ".css", ".html", ".svg", ".json":
		if acceptsGzip(r) && len(data) > 1024 {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Add("Vary", "Accept-Encoding")
			data = gzipped(name, data)
		}
	}
	_, _ = w.Write(data)
}

// Run serves until ctx ends.
func Run(ctx context.Context, address string, handler http.Handler) error {
	srv := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the language servers.
func (s *Server) Close() {
	s.lsps.Close()
	s.terms.Close()
	s.idxMu.Lock()
	for _, x := range s.indexes {
		x.Close()
	}
	s.idxMu.Unlock()
}
