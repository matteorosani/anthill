//go:build windows

package testkit

import "os"

// statusAccessViolation is the exit status of a Windows process that
// read memory it does not own.
var statusAccessViolation uint32 = 0xC0000005

// crash ends the agent with an access violation status, as Windows ends
// a process that crashes.
func crash() {
	os.Exit(int(int32(statusAccessViolation)))
}
