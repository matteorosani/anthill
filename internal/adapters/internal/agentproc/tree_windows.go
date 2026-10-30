//go:build windows

package agentproc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tree is the agent's Job Object: every process the agent starts belongs
// to it, and closing it kills them all, so the agents also end when
// Anthill itself ends.
type tree struct {
	job windows.Handle
	pid uint32
}

// getConsoleWindow tells whether Anthill has a console window.
var getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// prepare starts the process suspended, so that it is in the job before
// it can start anything, in a process group of its own, so that
// CTRL_BREAK reaches it and not Anthill. Without a console window of
// Anthill's own, the agent gets none either, so that none opens on the
// developer's desktop.
func prepare(cmd *exec.Cmd) {
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP)
	if hwnd, _, _ := getConsoleWindow.Call(); hwnd == 0 {
		flags |= windows.CREATE_NO_WINDOW
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

// attach puts the suspended process in a new job that kills its
// processes when it is closed, then resumes it.
func attach(cmd *exec.Cmd) (*tree, error) {
	pid := uint32(cmd.Process.Pid)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("creating the job object: %w", err)
	}
	t := &tree{job: job, pid: pid}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.close()
		return nil, fmt.Errorf("setting up the job object: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		t.close()
		return nil, fmt.Errorf("opening the agent process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		t.close()
		return nil, fmt.Errorf("adding the agent to the job object: %w", err)
	}
	if err := resume(pid); err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

// resume resumes the threads of a process started suspended: os/exec
// does not give their handles, so they are found in a snapshot.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("listing the agent's threads: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("opening the agent's thread: %w", err)
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return fmt.Errorf("resuming the agent: %w", err)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("listing the agent's threads: %w", err)
	}
	if resumed == 0 {
		return errors.New("resuming the agent: no thread found")
	}
	return nil
}

// interrupt sends CTRL_BREAK to the agent's process group. It reaches
// the agent only when it shares Anthill's console; otherwise the grace
// period just passes before kill.
func (t *tree) interrupt() error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, t.pid)
}

// kill ends every process of the job.
func (t *tree) kill() error {
	if t.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(t.job, 1)
}

// close closes the job, which kills what is left in it.
func (t *tree) close() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// crashed reports whether the process ended with an NTSTATUS error
// code, such as an access violation, rather than an exit code of its
// own.
func crashed(state *os.ProcessState) bool {
	return uint32(state.ExitCode()) >= 0xC0000000
}
