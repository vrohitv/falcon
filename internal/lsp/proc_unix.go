//go:build !windows

package lsp

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the server in its own process group, so stopping it stops what it started too.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree stops the server and everything in its group.
func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
