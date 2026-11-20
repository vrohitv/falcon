// Package term runs terminal sessions: a shell on a pseudo-terminal (a Unix pty, or a Windows
// pseudo-console), kept on the server so a page can leave and come back to it.
package term

import "io"

// PTY is a shell running on a pseudo-terminal: what it prints is read, what is typed is written.
type PTY interface {
	io.ReadWriter
	// Resize tells the program the terminal's new size (it redraws).
	Resize(cols, rows int) error
	// Wait waits for the shell to exit and gives its exit code.
	Wait() (int, error)
	// Kill stops the shell and everything it started, and frees the terminal.
	Kill()
}

// Start runs argv in dir on a new pseudo-terminal of the given size.
func Start(argv []string, dir string, env []string, cols, rows int) (PTY, error) {
	return start(argv, dir, env, clamp(cols, 2, 1000), clamp(rows, 1, 1000))
}

func clamp(n, lo, hi int) int {
	return max(lo, min(hi, n))
}
