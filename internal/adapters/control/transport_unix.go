//go:build !windows

package control

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// SocketName is the control socket's file name in <anthill-home>.
const SocketName = "control.sock"

// DefaultAddress returns the control socket of the current user:
// control.sock in home, the user's <anthill-home>.
func DefaultAddress(home string) (string, error) {
	return filepath.Join(home, SocketName), nil
}

// maxSocketPath is the size of sun_path, which holds a socket's path and
// its terminating zero byte: 108 on Linux, 104 on macOS and the BSDs.
func maxSocketPath() int {
	if runtime.GOOS == "linux" {
		return 108
	}
	return 104
}

// Listen listens on the Unix socket at address. Its directory is made
// private to the user (mode 0700) and the socket itself mode 0600, so no
// other user can connect. A socket left by a daemon that died is
// replaced: the caller holds the daemon lock, so no live daemon uses it.
func Listen(address string) (net.Listener, error) {
	if limit := maxSocketPath() - 1; len(address) > limit {
		return nil, fmt.Errorf("control socket path %s is too long: %d bytes, the limit on %s is %d", address, len(address), runtime.GOOS, limit)
	}
	dir := filepath.Dir(address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the control socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the control socket directory private: %w", err)
	}
	if info, err := os.Lstat(address); err == nil {
		if info.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("%s exists and is not a socket", address)
		}
		if err := os.Remove(address); err != nil {
			return nil, fmt.Errorf("removing the old control socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(address, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("making the control socket private: %w", err)
	}
	return ln, nil
}

func dial(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", address)
}

// notRunning reports whether a dial error means that no daemon listens:
// the socket is missing, or left by a daemon that died.
func notRunning(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}
