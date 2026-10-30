package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

func TestClaimLossHandler(t *testing.T) {
	h := &testkit.ClaimLossHandler{Err: errors.New("supervisor busy")}
	if err := h.ClaimLost(context.Background(), item12, domain.ClaimTask, "claim lost"); err == nil || len(h.Losses()) != 0 {
		t.Errorf("failing call: err = %v, losses = %v", err, h.Losses())
	}
	h.Err = nil
	if err := h.ClaimLost(context.Background(), item12, domain.ClaimTask, "claim lost"); err != nil {
		t.Fatal(err)
	}
	if got := h.Losses(); len(got) != 1 || got[0] != (testkit.ClaimLoss{Ref: item12, Kind: domain.ClaimTask, Reason: "claim lost"}) {
		t.Errorf("Losses() = %v", got)
	}
}
