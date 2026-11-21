package main

import (
	"context"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

func proposalList(warnings []string, items ...int) app.ProposalList {
	list := app.ProposalList{Warnings: warnings}
	for _, n := range items {
		list.Proposals = append(list.Proposals, app.Proposal{Item: shopItem(n), Title: "Fix bug"})
	}
	return list
}

func titles(sent []ports.Notification) []string {
	var out []string
	for _, n := range sent {
		out = append(out, n.Title)
	}
	return out
}

// Spec queen-console, "Notifications": the first evaluation after start
// tells about the whole list in one notification; later evaluations tell
// only about items new to the list, including one that left the list
// and came back, such as an un-hidden item.
func TestProposalWatchNotifiesNewProposals(t *testing.T) {
	ctx := context.Background()
	n := &testkit.Notifier{}
	w := newProposalWatch(n)

	w.update(ctx, "", proposalList(nil, 12, 13))
	w.update(ctx, domain.ConnectionGitHub, proposalList(nil, 12, 13))
	w.update(ctx, domain.ConnectionGitHub, proposalList(nil, 12, 13, 14))
	w.update(ctx, domain.ConnectionGitHub, proposalList(nil, 12, 14))
	w.update(ctx, domain.ConnectionGitHub, proposalList(nil, 12, 13, 14))

	got := titles(n.Sent())
	want := []string{"2 new proposals", "New proposal: #14", "New proposal: #13"}
	if len(got) != len(want) {
		t.Fatalf("notifications = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notification %d = %q, want %q", i, got[i], want[i])
		}
	}
	if s := n.Sent(); s[0].Body != "#12 Fix bug\n#13 Fix bug" || s[0].Kind != ports.NotifyProposal {
		t.Errorf("first notification = %+v", s[0])
	}
}

// An incomplete list, from a tracker that could not be read, does not
// forget the items missing from it, so they raise nothing when the
// tracker answers again. Another connection's evaluation leaves this
// connection's items alone.
func TestProposalWatchKeepsItemsOfIncompleteLists(t *testing.T) {
	ctx := context.Background()
	n := &testkit.Notifier{}
	w := newProposalWatch(n)
	w.update(ctx, "", proposalList(nil, 12))
	w.update(ctx, domain.ConnectionGitHub, proposalList([]string{"listing failed"}))
	w.update(ctx, "azure-devops/contoso", proposalList(nil))
	w.update(ctx, domain.ConnectionGitHub, proposalList(nil, 12))
	if got := titles(n.Sent()); len(got) != 1 {
		t.Errorf("notifications = %q, want only the first", got)
	}
}

// Many new proposals are counted, and the first few named.
func TestProposalNotificationNamesTheFirstFew(t *testing.T) {
	var fresh []app.Proposal
	for i := range maxListedProposals + 2 {
		fresh = append(fresh, app.Proposal{Item: shopItem(10 + i), Title: "T"})
	}
	got := proposalNotification(fresh)
	if got.Title != "7 new proposals" || got.Ref != nil {
		t.Errorf("notification = %+v", got)
	}
	if want := "#10 T\n#11 T\n#12 T\n#13 T\n#14 T\nand 2 more"; got.Body != want {
		t.Errorf("body = %q, want %q", got.Body, want)
	}
}

// The notifications setting turns the kind off.
func TestProposalWatchFollowsTheNotificationsSetting(t *testing.T) {
	ctx := context.Background()
	config := app.NewConfiguration(&testkit.ConfigSource{Config: ports.LocalConfig{
		Global: domain.SettingValues{Notifications: map[string]bool{string(domain.NotifyProposal): false}},
	}})
	if _, err := config.Load(ctx); err != nil {
		t.Fatal(err)
	}
	desktop := &testkit.Notifier{}
	w := newProposalWatch(app.NewNotifications(config, desktop))
	w.update(ctx, "", proposalList(nil, 12))
	if got := desktop.Sent(); len(got) != 0 {
		t.Errorf("notifications = %+v, want none", got)
	}
}
