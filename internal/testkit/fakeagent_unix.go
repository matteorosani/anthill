//go:build !windows

package testkit

import (
	"os"
	"syscall"
)

// crash kills the agent with SIGKILL, as the system does to a process
// that misbehaves.
func crash() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}
