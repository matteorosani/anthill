// Package lockfile keeps one daemon per user and machine (spec
// queen-console, "Daemon runs the loops"; design D10). The daemon holds
// an OS lock on a lock file for as long as it runs: flock on Unix and
// LockFileEx on Windows. The OS drops that lock when the process ends,
// however it ends, so a held lock means a live daemon, and a lock file
// left by a dead process is taken over without a check of its pid. The
// file holds the daemon's pid only for the message a second start prints.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HeldError is returned when another live process holds the lock.
type HeldError struct {
	// PID is the holder's process id, or 0 when the file did not name
	// one yet.
	PID int
}

func (e *HeldError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("daemon already running (pid %d)", e.PID)
	}
	return "daemon already running"
}

// errLocked is what lockFile returns when another process holds the lock.
var errLocked = errors.New("lock held by another process")

// Lock is a held lock file.
type Lock struct {
	file *os.File
}

// Acquire takes the lock at path, creating the file and its directory
// (mode 0700) when missing, and writes the current pid into it. When a
// live process holds the lock, it returns a *HeldError naming that
// process.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLocked) {
			return nil, &HeldError{PID: readPID(path)}
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	// Whatever a dead process left in the file is replaced.
	if err := writePID(f); err != nil {
		_ = unlockFile(f)
		_ = f.Close()
		return nil, fmt.Errorf("writing the lock file: %w", err)
	}
	return &Lock{file: f}, nil
}

// Release empties the lock file and gives up the lock. The file itself
// stays: removing it could let two processes lock two different files
// with the same name.
func (l *Lock) Release() error {
	truncErr := l.file.Truncate(0)
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	return errors.Join(truncErr, unlockErr, closeErr)
}

func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return err
	}
	return f.Sync()
}

// readPID returns the pid written in the lock file, or 0 when it cannot
// be read, for example because the holder has not written it yet.
func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
