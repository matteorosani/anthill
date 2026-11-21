//go:build windows

package verify

import (
	"os"
	"path/filepath"
)

// shell writes the command to a batch file and runs it with cmd /C.
// cmd.exe does not read Go's quoting of a /C argument, so a command with
// quotes of its own would reach it changed; a batch file holds it as
// written.
func shell(cmd string) (command, error) {
	dir, err := os.MkdirTemp("", "anthill-verify-")
	if err != nil {
		return command{}, err
	}
	file := filepath.Join(dir, "verify.cmd")
	if err := os.WriteFile(file, []byte("@echo off\r\n"+cmd+"\r\nexit /b %ERRORLEVEL%\r\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return command{}, err
	}
	return command{path: "cmd", args: []string{"/D", "/C", file}, cleanup: func() { _ = os.RemoveAll(dir) }}, nil
}
