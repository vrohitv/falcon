// Package sysenv is the environment Falcon gives the programs it runs (git, language servers): only what
// they need to work on this system, never the owner's secrets.
package sysenv

import (
	"os"
	"runtime"
)

// basic are the variables every program needs: where programs are, the home folder, temporary files; on
// Windows also the system folders without which git and its helpers cannot start.
var basic = []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP",
	"SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
	"HOMEDRIVE", "HOMEPATH", "PROGRAMDATA", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "CommonProgramFiles",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "USERNAME", "USERDOMAIN"}

// Base is the basic environment, plus extra (NAME=value).
func Base(extra ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, k := range basic {
		if v, ok := os.LookupEnv(k); ok && !seen[k] {
			seen[k] = true
			out = append(out, k+"="+v)
		}
	}
	if runtime.GOOS != "windows" {
		out = append(out, "LANG=C.UTF-8")
	}
	return append(out, extra...)
}

// Null is the empty file (/dev/null, or NUL on Windows), for settings that must point at nothing.
var Null = os.DevNull

// Git is the environment for a git command that ignores the owner's global and system configuration.
func Git(extra ...string) []string {
	return Base(append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + Null, "GIT_OPTIONAL_LOCKS=0"}, extra...)...)
}
