package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
)

// Responses worth compressing: JSON over 16 KB (a big folder's file list is megabytes) and the UI's own
// files. Streams (server-sent events) are left alone; small answers are not worth the time.
const gzipMin = 16 << 10

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// gzipJSON wraps a writer so a large JSON body is compressed. The JSON encoder writes the whole body in
// one call, so the decision is made on that first write.
type gzipJSON struct {
	http.ResponseWriter
	decided bool
	status  int
}

func (g *gzipJSON) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzipJSON) Write(b []byte) (int, error) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if strings.HasPrefix(h.Get("Content-Type"), "application/json") && len(b) >= gzipMin && h.Get("Content-Encoding") == "" {
			h.Set("Content-Encoding", "gzip")
			h.Add("Vary", "Accept-Encoding")
			h.Del("Content-Length")
			g.flushHeader()
			zw, _ := gzip.NewWriterLevel(g.ResponseWriter, gzip.BestSpeed)
			_, err := zw.Write(b)
			if cerr := zw.Close(); err == nil {
				err = cerr
			}
			return len(b), err
		}
		g.flushHeader()
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipJSON) flushHeader() {
	if g.status != 0 {
		g.ResponseWriter.WriteHeader(g.status)
	}
}

// Flush passes through, for streams.
func (g *gzipJSON) Flush() {
	if !g.decided {
		g.decided = true
		g.flushHeader()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// compressed caches the UI's files compressed, by name and content.
var compressed sync.Map // string -> []byte

func gzipped(name string, data []byte) []byte {
	sum := sha256.Sum256(data)
	key := name + "\x00" + string(sum[:8])
	if v, ok := compressed.Load(key); ok {
		return v.([]byte)
	}
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zw.Write(data)
	zw.Close()
	out := b.Bytes()
	compressed.Store(key, out)
	return out
}
