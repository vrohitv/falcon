//go:build windows

package term

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows pseudo-console (ConPTY, Windows 10 1809 and later): the shell's console is a pair of
// pipes Falcon reads and writes, and the console translates to and from terminal escape sequences.
type winPTY struct {
	console windows.Handle
	process windows.Handle
	pid     uint32
	in      *os.File // what is typed
	out     *os.File // what the console prints
	once    sync.Once
	// The console keeps its end of the output pipe open after the shell exits, so the reader would wait
	// forever: the shell's exit is watched instead, and closes the console (which ends the output).
	exited  chan struct{}
	code    uint32
	closing sync.Once
}

// closeConsole closes the pseudo-console once: it sends what is left of the screen, ends whatever is
// still attached, and closes the output pipe, so a pending Read returns.
func (p *winPTY) closeConsole() {
	p.closing.Do(func() { windows.ClosePseudoConsole(p.console) })
}

func start(argv []string, dir string, env []string, cols, rows int) (PTY, error) {
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, err
	}
	var console windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inRead, outWrite, 0, &console)
	// The console holds its own ends of the pipes now.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	if err != nil {
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return nil, err
	}
	p := &winPTY{console: console, exited: make(chan struct{}), in: os.NewFile(uintptr(inWrite), "conpty-in"), out: os.NewFile(uintptr(outRead), "conpty-out")}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		p.Kill()
		return nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the console handle itself (not a pointer to it), as the API documents.
	value := *(*unsafe.Pointer)(unsafe.Pointer(&console))
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, value, unsafe.Sizeof(console)); err != nil {
		p.Kill()
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// Without this a child inherits Falcon's own standard handles whenever they are redirected (output to a
	// file or pipe, run from a service or a test) and prints there instead of on the pseudo-console.
	si.Flags |= windows.STARTF_USESTDHANDLES
	// The program is found as a terminal would find it (PATH, PATHEXT).
	if path, err := exec.LookPath(argv[0]); err == nil {
		argv = append([]string{path}, argv[1:]...)
	}
	line, err := windows.UTF16PtrFromString(commandLine(argv))
	if err != nil {
		p.Kill()
		return nil, err
	}
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			p.Kill()
			return nil, err
		}
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err = windows.CreateProcess(nil, line, nil, nil, false, flags, envBlock(env), cwd, &si.StartupInfo, &pi); err != nil {
		p.Kill()
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	p.process, p.pid = pi.Process, pi.ProcessId
	go func() {
		_, _ = windows.WaitForSingleObject(p.process, windows.INFINITE)
		if windows.GetExitCodeProcess(p.process, &p.code) != nil {
			p.code = 1
		}
		close(p.exited)
		p.closeConsole()
	}()
	return p, nil
}

func (p *winPTY) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err != nil && (errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, os.ErrClosed)) {
		err = os.ErrClosed
	}
	return n, err
}
func (p *winPTY) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *winPTY) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.console, windows.Coord{X: int16(clamp(cols, 2, 1000)), Y: int16(clamp(rows, 1, 1000))})
}
func (p *winPTY) Wait() (int, error) {
	if p.process == 0 {
		return -1, errors.New("not started")
	}
	<-p.exited
	return int(p.code), nil
}
func (p *winPTY) Kill() {
	p.once.Do(func() {
		if p.pid != 0 {
			// The shell and everything it started.
			kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(int(p.pid)))
			kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 /* CREATE_NO_WINDOW */}
			_ = kill.Run()
		}
		if p.process != 0 {
			_ = windows.TerminateProcess(p.process, 1)
		}
		// Closing the console ends what is still attached and unblocks the reader.
		p.closeConsole()
		_ = p.in.Close()
		_ = p.out.Close()
		if p.process != 0 {
			// The handle is the exit watcher's until the shell has gone.
			select {
			case <-p.exited:
				windows.CloseHandle(p.process)
			case <-time.After(5 * time.Second):
			}
		}
	})
}

// commandLine joins arguments as Windows programs parse them.
func commandLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = windows.EscapeArg(a)
	}
	return strings.Join(parts, " ")
}

// envBlock is an environment as CreateProcess takes it: NAME=value strings, each ended by a NUL, and one
// more NUL at the end.
func envBlock(env []string) *uint16 {
	if len(env) == 0 {
		return nil
	}
	var b []uint16
	for _, kv := range env {
		u, err := windows.UTF16FromString(kv) // ends with its NUL
		if err != nil {
			continue
		}
		b = append(b, u...)
	}
	b = append(b, 0)
	return &b[0]
}
