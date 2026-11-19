package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLargeJSONIsCompressedSmallIsNot(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", 40<<10) + `"}`
	for _, body := range []string{big, `{"ok":true}`} {
		w := httptest.NewRecorder()
		g := &gzipJSON{ResponseWriter: w}
		g.Header().Set("Content-Type", "application/json")
		g.WriteHeader(201)
		g.Write([]byte(body))
		if w.Code != 201 {
			t.Fatalf("status lost: %d", w.Code)
		}
		got := w.Body.String()
		if body == big {
			if w.Header().Get("Content-Encoding") != "gzip" {
				t.Fatal("large JSON not compressed")
			}
			zr, err := gzip.NewReader(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(zr)
			got = string(b)
		} else if w.Header().Get("Content-Encoding") != "" {
			t.Fatal("small JSON compressed")
		}
		if got != body {
			t.Fatal("body changed")
		}
	}
}

func TestUIFilesAreServedCompressed(t *testing.T) {
	h := newHarness(t, "")
	dist := t.TempDir()
	writeFile(t, dist+"/index.html", "<!doctype html>"+strings.Repeat("falcon ", 500))
	h.s.Assets = dist
	r := httptest.NewRequest("GET", "http://127.0.0.1:7700/", nil)
	r.Host = "127.0.0.1:7700"
	r.Header.Set("Accept-Encoding", "gzip, br")
	w := httptest.NewRecorder()
	h.s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() > 500 {
		t.Fatalf("%d %v %d", w.Code, w.Header(), w.Body.Len())
	}
}
