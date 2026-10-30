package desktop

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gen2brain/beeep"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// shown is one call of the fake show function.
type shown struct {
	title, message string
}

// fakeNotifier returns a notifier whose show function records its calls
// and returns err, so that no test pops a real notification.
func fakeNotifier(err error) (*Notifier, *[]shown, *bytes.Buffer) {
	var calls []shown
	var logs bytes.Buffer
	n := &Notifier{
		show: func(title, message string, _ any) error {
			calls = append(calls, shown{title, message})
			return err
		},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}
	return n, &calls, &logs
}

func TestNotifyShowsTitleAndBody(t *testing.T) {
	n, calls, _ := fakeNotifier(nil)
	err := n.Notify(context.Background(), ports.Notification{
		Kind:  ports.NotifyExtension,
		Title: "#12 asks for 15 more minutes",
		Body:  "The migration needs another pass.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []shown{{"#12 asks for 15 more minutes", "The migration needs another pass."}}
	if len(*calls) != 1 || (*calls)[0] != want[0] {
		t.Errorf("shown %+v, want %+v", *calls, want)
	}
}

// A notification that cannot be shown is logged once and not retried.
func TestNotifyLogsFailureWithoutRetry(t *testing.T) {
	failure := errors.New("no notification service")
	n, calls, logs := fakeNotifier(failure)
	ref := domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}, Kind: domain.KindItem, Number: 12}
	err := n.Notify(context.Background(), ports.Notification{Kind: ports.NotifyClaimLost, Title: "Claim lost", Ref: &ref})
	if !errors.Is(err, failure) {
		t.Errorf("err = %v, want %v", err, failure)
	}
	if len(*calls) != 1 {
		t.Errorf("shown %d times, want 1", len(*calls))
	}
	for _, want := range []string{"desktop notification not shown", "kind=claim-lost", "no notification service", "item=gh-12"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log %q does not contain %q", logs.String(), want)
		}
	}
}

// New only sets beeep up; it shows nothing.
func TestNewUsesTheAppName(t *testing.T) {
	if n := New(slog.Default()); n.show == nil {
		t.Fatal("no show function")
	}
	if beeep.AppName != AppName {
		t.Errorf("beeep.AppName = %q, want %q", beeep.AppName, AppName)
	}
}
