package term

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"
)

// read collects a viewer's output until it contains want (or the time runs out).
func read(t *testing.T, v *Viewer, want string, within time.Duration) string {
	t.Helper()
	var got bytes.Buffer
	deadline := time.After(within)
	out := make(chan []byte)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			b := v.Next(64 << 10)
			if b == nil {
				close(out)
				return
			}
			v.Ack(len(b))
			select {
			case out <- b:
			case <-stop:
				return
			}
		}
	}()
	for {
		select {
		case b, ok := <-out:
			if !ok {
				return got.String()
			}
			got.Write(b)
			if strings.Contains(got.String(), want) {
				return got.String()
			}
		case <-deadline:
			t.Fatalf("waited for %q; got %q", want, got.String())
		}
	}
}

func shellLine(cmd string) string {
	if runtime.GOOS == "windows" {
		return cmd + "\r\n"
	}
	return cmd + "\n"
}

// onWindows is true where the terminal's shell is PowerShell (or cmd) on a pseudo-console, not a POSIX
// shell on a pty: the commands typed differ, and the console redraws its screen rather than passing every
// byte through.
var onWindows = runtime.GOOS == "windows"

func TestSessionRunsAShellAndComesBackAfterDetaching(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("FALCON_TOKEN", "secret-token-value-123456789")
	m := NewManager()
	defer m.Close()
	dir := t.TempDir()
	s, err := m.Open(dir, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Attach()
	// Each answer is computed, so the terminal's echo of the command line does not contain it.
	first, inFolder := "echo falcon-$((40+2)); pwd; echo token=[$FALCON_TOKEN] term=$TERM", strings.TrimPrefix(dir, "/private")
	if onWindows {
		// A long path wraps on an 80-column console, so the folder is compared by the shell itself.
		first = "echo \"falcon-$(40+2)\"; echo (\"in-\" + ((Get-Location).Path -eq '" + dir + "')); echo \"token=[$env:FALCON_TOKEN] term=$env:TERM\""
		inFolder = "in-True"
	}
	s.Write([]byte(shellLine(first)))
	out := read(t, v, "term=xterm-256color", 10*time.Second)
	if !strings.Contains(out, "falcon-42") {
		t.Fatalf("the shell did not run the command: %q", out)
	}
	if !strings.Contains(out, "token=[]") {
		t.Fatalf("Falcon's token must not reach the terminal: %q", out)
	}
	// The shell starts in the folder (macOS's temporary folders resolve through /private).
	if !strings.Contains(out, inFolder) {
		t.Fatalf("not started in the folder %s: %q", dir, out)
	}
	// A page leaves and comes back: the output so far is there again.
	s.Detach(v)
	s.Write([]byte(shellLine("echo after-detach")))
	time.Sleep(300 * time.Millisecond)
	v2 := m.Get(dir, s.ID).Attach()
	back := read(t, v2, "after-detach", 5*time.Second)
	if !strings.Contains(back, "falcon-42") {
		t.Fatalf("the backlog was lost: %q", back)
	}
	// Resizing is seen by the program.
	if err := s.Resize(132, 40); err != nil {
		t.Fatal(err)
	}
	if onWindows {
		s.Write([]byte(shellLine("$z = $Host.UI.RawUI.WindowSize; echo \"size-$($z.Height)x$($z.Width)\"")))
		read(t, v2, "size-40x132", 5*time.Second)
	} else {
		s.Write([]byte(shellLine("stty size")))
		read(t, v2, "40 132", 5*time.Second)
	}
	// Listing, then exit: the session goes away.
	if l := m.List(dir); len(l) != 1 || l[0].ID != s.ID || l[0].Cols != 132 {
		t.Fatalf("list: %+v", l)
	}
	if m.List(t.TempDir()) == nil || len(m.List(t.TempDir())) != 0 {
		t.Fatal("another folder sees no sessions")
	}
	s.Write([]byte(shellLine("exit 3")))
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the shell did not exit")
	}
	if s.Code() != 3 {
		t.Fatalf("exit code %d", s.Code())
	}
	time.Sleep(50 * time.Millisecond)
	if m.Get(dir, s.ID) != nil {
		t.Fatal("an exited session is forgotten")
	}
}

// A viewer that does not keep up pauses the shell instead of piling output up in memory; what it
// missed arrives once it catches up, nothing lost.
func TestSlowViewerPausesTheShellAndLosesNothing(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := NewManager()
	defer m.Close()
	s, err := m.Open(t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	v := s.Attach()
	// About 9 MB of numbered lines, then a marker.
	loop := "i=0; while [ $i -lt 300000 ]; do echo line-$i-xxxxxxxxxxxxxxxx; i=$((i+1)); done; echo END-OF-$((1+1))"
	if onWindows {
		// Built first and printed as one write: PowerShell printing 300,000 items one by one takes minutes.
		loop = "$a = foreach ($i in 0..299999) { \"line-$i-xxxxxxxxxxxxxxxx\" }; $a -join \"`n\"; echo \"END-OF-$(1+1)\""
	}
	s.Write([]byte(shellLine(loop)))
	time.Sleep(1500 * time.Millisecond)
	// Nothing was acknowledged: no more than the window (plus one read) is waiting for this viewer.
	v.mu.Lock()
	held := v.queue.Len() + v.inflight
	v.mu.Unlock()
	if held > viewerWindow+(64<<10) {
		t.Fatalf("a slow viewer holds %d bytes; the shell should have paused", held)
	}
	// The marker is computed, so the terminal's echo of the command line does not contain it.
	out := read(t, v, "END-OF-2", 120*time.Second)
	if onWindows {
		// The pseudo-console is a screen, not a stream: while nobody reads, it keeps drawing into its own
		// buffer and then sends only what is on screen, so lines that scrolled away meanwhile never come.
		// What holds is that the shell waited for the viewer and finished.
		return
	}
	for _, want := range []string{"line-0-", "line-150000-", "line-299999-"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestKillEndsTheShellAndWhatItStarted(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := NewManager()
	s, err := m.Open(t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.Write([]byte(shellLine("sleep 300")))
	time.Sleep(200 * time.Millisecond)
	m.CloseFolder(s.Folder)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("kill did not end the session")
	}
}
