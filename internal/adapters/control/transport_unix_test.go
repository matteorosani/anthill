//go:build !windows

package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only the user can reach the socket: its directory has mode 0700 and the
// socket mode 0600.
func TestSocketIsPrivate(t *testing.T) {
	address, _ := startServer(t, func(*Server) {})
	for path, want := range map[string]os.FileMode{filepath.Dir(address): 0o700, address: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
}

func TestSocketPathTooLong(t *testing.T) {
	address := "/" + strings.Repeat("a", maxSocketPath()) + "/control.sock"
	_, err := Listen(address)
	if err == nil || !strings.Contains(err.Error(), address) || !strings.Contains(err.Error(), "too long") {
		t.Errorf("err = %v, want a too-long error naming the path", err)
	}
}

// A socket left by a daemon that died refuses connections, which clients
// report as no daemon, and the next daemon replaces it.
func TestStaleSocket(t *testing.T) {
	address := testAddress(t)
	if err := os.MkdirAll(filepath.Dir(address), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()

	if err := NewClient(address).Call(context.Background(), "status", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	ln, err = Listen(address)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	_ = ln.Close()
}

// A file that is not a socket is never removed.
func TestListenKeepsOtherFiles(t *testing.T) {
	address := testAddress(t)
	if err := os.MkdirAll(filepath.Dir(address), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(address, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(address); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(address); string(data) != "notes" {
		t.Error("file was changed")
	}
}

func TestDefaultAddress(t *testing.T) {
	got, err := DefaultAddress(filepath.FromSlash("/home/queen/.anthill"))
	if err != nil || got != filepath.FromSlash("/home/queen/.anthill/control.sock") {
		t.Errorf("DefaultAddress = %q, %v", got, err)
	}
}
