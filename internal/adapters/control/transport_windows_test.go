//go:build windows

package control

import (
	"strings"
	"testing"
)

// The pipe is named after the user's SID and only that SID may open it.
func TestDefaultAddress(t *testing.T) {
	got, err := DefaultAddress("")
	if err != nil {
		t.Fatal(err)
	}
	sid := strings.TrimPrefix(got, pipePrefix)
	if sid == got || !strings.HasPrefix(sid, "S-1-") {
		t.Errorf("DefaultAddress = %q, want %s<SID>", got, pipePrefix)
	}
	if sd := securityDescriptor(sid); sd != "D:P(A;;GA;;;"+sid+")" {
		t.Errorf("security descriptor = %q", sd)
	}
}
