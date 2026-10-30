//go:build windows

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRange returns where the lock lies: one byte at 4 GiB, far past the
// pid, because Windows locks are mandatory and a second process must
// still be able to read the pid for its message.
func lockRange() *windows.Overlapped {
	return &windows.Overlapped{OffsetHigh: 1}
}

// lockFile takes an exclusive lock on f without waiting.
func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
