package server

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vrohitv/falcon/internal/workspace"
)

// rawTypes are the files the viewers show as themselves (images, PDF, audio, video, fonts). Anything else
// is sent as plain text or bytes, never as something a browser would run.
var rawTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon", ".svg": "image/svg+xml",
	".pdf": "application/pdf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".m4v": "video/mp4",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".flac": "audio/flac", ".m4a": "audio/mp4",
	".ttf": "font/ttf", ".otf": "font/otf", ".woff": "font/woff", ".woff2": "font/woff2",
}

// raw sends a file's bytes for a viewer: with byte ranges (PDF and video seek), its real type when it is
// media, and a policy that lets nothing in it run (an SVG's scripts, an HTML file's anything).
func (s *Server) raw(w http.ResponseWriter, r *http.Request, rp *workspace.Repo) {
	p := r.URL.Query().Get("path")
	abs, err := rp.Repo.ReadPath(p)
	if err != nil {
		fail(w, err)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fail(w, errors.New("not a regular file"))
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	ctype, media := rawTypes[ext]
	if !media {
		ctype = "text/plain; charset=utf-8"
		if t := mime.TypeByExtension(ext); t != "" && !strings.HasPrefix(t, "text/") && !strings.Contains(t, "html") && !strings.Contains(t, "javascript") && !strings.Contains(t, "xml") {
			ctype = "application/octet-stream"
		}
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	// Shown inside Falcon only; nothing in the file may run, load anything, or be framed elsewhere.
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; media-src 'self'; font-src 'self'; sandbox; frame-ancestors 'self'")
	if ext == ".pdf" {
		// The browser's own PDF viewer needs scripts in its sandboxed frame.
		h.Set("Content-Security-Policy", "frame-ancestors 'self'")
	}
	h.Set("Content-Disposition", "inline; filename*=UTF-8''"+strings.ReplaceAll(filepath.Base(p), " ", "%20"))
	http.ServeContent(w, r, "", info.ModTime(), f)
}
