//go:build windows

package control

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipePrefix starts the name of every Anthill pipe; the user's SID ends
// it.
const pipePrefix = `\\.\pipe\anthill-`

// DefaultAddress returns the control pipe of the current user,
// \\.\pipe\anthill-<SID>. The SID makes it unique per user, and home is
// not used.
func DefaultAddress(string) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return pipePrefix + sid, nil
}

// Listen listens on the named pipe at address. Its security descriptor
// grants access to the current user only, and go-winio creates every
// pipe with PIPE_REJECT_REMOTE_CLIENTS, so a connection from another
// machine is refused.
func Listen(address string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(address, &winio.PipeConfig{SecurityDescriptor: securityDescriptor(sid)})
}

// securityDescriptor returns the SDDL that grants full access to sid and
// to nobody else: a protected DACL with one allow entry.
func securityDescriptor(sid string) string {
	return "D:P(A;;GA;;;" + sid + ")"
}

func dial(ctx context.Context, address string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, address)
}

// notRunning reports whether a dial error means that no daemon listens:
// the pipe does not exist.
func notRunning(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// currentUserSID returns the SID of the user running this process, such
// as S-1-5-21-…-1001.
func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading the current user: %w", err)
	}
	return user.User.Sid.String(), nil
}
