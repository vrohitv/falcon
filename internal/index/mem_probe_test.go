package index

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/vrohitv/falcon/internal/repository"
)

func TestMemoryProbe(t *testing.T) {
	dir := os.Getenv("FALCON_PROBE_REPO")
	if dir == "" {
		t.Skip()
	}
	r, err := repository.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	heap := func() float64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc) / (1 << 20)
	}
	base := heap()
	x := New(r, "")
	x.Refresh(context.Background())
	sym := heap()
	x.BuildText()
	for !x.TextReady() {
		time.Sleep(50 * time.Millisecond)
	}
	text := heap()
	st := x.TextStatus()
	t.Logf("symbols: %.0f MB, text: %.0f MB (files %d, postings %d)", sym-base, text-sym, st.Files, st.Postings)
}
