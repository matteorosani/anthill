package lockfile_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/lockfile"
)

// helperEnv makes the test binary act as a second process that holds the
// lock at the given path until its standard input closes.
const helperEnv = "ANTHILL_LOCKFILE_TEST_HOLDER"

func TestMain(m *testing.M) {
	if path := os.Getenv(helperEnv); path != "" {
		os.Exit(holdLock(path))
	}
	os.Exit(m.Run())
}

func holdLock(path string) int {
	lock, err := lockfile.Acquire(path)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		return 1
	}
	return 0
}

// startHolder starts a process that holds the lock at path, and waits
// until it does.
func startHolder(t *testing.T, path string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("holder said %q, %v", line, err)
	}
	return cmd, stdin
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAcquireWritesThePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "daemon.lock")
	lock, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), strconv.Itoa(os.Getpid())+"\n"; got != want {
		t.Errorf("lock file holds %q, want %q", got, want)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	// Released, the lock can be taken again.
	lock, err = lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Release()
}

// Scenario "Second daemon start": while another live process holds the
// lock, taking it fails with that process's pid in the message.
func TestSecondDaemonStartAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder, _ := startHolder(t, path)

	_, err := lockfile.Acquire(path)
	var held *lockfile.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want a HeldError", err)
	}
	if held.PID != holder.Process.Pid {
		t.Errorf("pid = %d, want %d", held.PID, holder.Process.Pid)
	}
	want := fmt.Sprintf("daemon already running (pid %d)", holder.Process.Pid)
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err, want)
	}
}

// Within one process, a second Acquire also sees the lock as held: the
// OS lock belongs to the open file, not to the process.
func TestSecondAcquireInTheSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	_, err = lockfile.Acquire(path)
	want := fmt.Sprintf("daemon already running (pid %d)", os.Getpid())
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// A lock file left by a process that was killed is taken over: the OS
// dropped the lock when the process died, whatever pid the file names.
func TestStaleLockFromDeadProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder, _ := startHolder(t, path)
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	if got := readFile(t, path); got != strconv.Itoa(holder.Process.Pid)+"\n" {
		t.Fatalf("the dead process left %q", got)
	}

	lock, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	defer func() { _ = lock.Release() }()
	if got, want := readFile(t, path), strconv.Itoa(os.Getpid())+"\n"; got != want {
		t.Errorf("lock file holds %q, want %q", got, want)
	}
}

// A lock file that names a pid nobody holds a lock for, such as one
// copied from another machine, is taken over too.
func TestStaleLockFileWithoutHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if got, want := readFile(t, path), strconv.Itoa(os.Getpid())+"\n"; got != want {
		t.Errorf("lock file holds %q, want %q", got, want)
	}
}

func TestLockDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	dir := filepath.Join(t.TempDir(), "home")
	lock, err := lockfile.Acquire(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
}

func TestHeldErrorWithoutPID(t *testing.T) {
	if got := (&lockfile.HeldError{}).Error(); got != "daemon already running" {
		t.Errorf("message = %q", got)
	}
}
