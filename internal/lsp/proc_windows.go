//go:build windows

package lsp

import (
	"os/exec"
	"strconv"
	"syscall"
)

// ownGroup starts the server in a new process group, without a console window.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000 /* CREATE_NO_WINDOW */}
}

// killTree stops the server and the processes it started (taskkill /T), then the server itself.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = kill.Run()
	_ = cmd.Process.Kill()
}
