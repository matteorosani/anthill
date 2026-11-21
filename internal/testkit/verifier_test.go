package testkit_test

import (
	"context"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestVerifier(t *testing.T) {
	v := &testkit.Verifier{}
	got, err := v.Verify(context.Background(), "/wt", "go test ./...", time.Minute)
	if err != nil || !got.Passed() {
		t.Fatalf("Verify() = %+v, %v", got, err)
	}
	v.Set(ports.Verification{Exit: ports.Exit{Code: 2}}, nil)
	if got, _ := v.Verify(context.Background(), "/wt", "make", time.Second); got.Passed() || got.Exit.Code != 2 {
		t.Errorf("Verify() = %+v", got)
	}
	if calls := v.Calls(); len(calls) != 2 || calls[1] != (testkit.VerifyCall{Dir: "/wt", Command: "make", Timeout: time.Second}) {
		t.Errorf("calls = %+v", calls)
	}
}
