//go:build !windows

package agentproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// tree is the agent's process group: the agent is its leader, and what
// it starts joins it unless it asks for a group of its own.
type tree struct {
	pgid int
}

// prepare makes the process the leader of a new process group, before
// it runs any of its own code.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// attach returns the tree of the started process.
func attach(cmd *exec.Cmd) (*tree, error) {
	return &tree{pgid: cmd.Process.Pid}, nil
}

// interrupt asks every process of the group to end.
func (t *tree) interrupt() error {
	return t.signal(syscall.SIGTERM)
}

// kill kills every process of the group. A group with no process left
// is not an error. The system does not reuse a group's id while a
// process of the group remains, so the signal reaches only the group.
func (t *tree) kill() error {
	return t.signal(syscall.SIGKILL)
}

func (t *tree) signal(sig syscall.Signal) error {
	// A negative pid sends the signal to the whole process group.
	err := syscall.Kill(-t.pgid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (t *tree) close() {}

// crashed reports whether a signal ended the process.
func crashed(state *os.ProcessState) bool {
	ws, ok := state.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}
