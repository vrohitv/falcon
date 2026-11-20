//go:build !windows

package term

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

type unixPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func start(argv []string, dir string, env []string, cols, rows int) (PTY, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	// pty.Start makes the shell a session leader on the terminal: Kill can reach everything it started.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	// Linux reports EIO once the shell has exited and the terminal is closed: that is the end.
	if err != nil && errors.Is(err, syscall.EIO) {
		err = os.ErrClosed
	}
	return n, err
}
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(clamp(cols, 2, 1000)), Rows: uint16(clamp(rows, 1, 1000))})
}
func (p *unixPTY) Wait() (int, error) {
	err := p.cmd.Wait()
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode(), nil
	}
	return -1, err
}
func (p *unixPTY) Kill() {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			// The whole session: the shell and what runs in it (a hung up terminal, then a kill).
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = p.f.Close()
	})
}
