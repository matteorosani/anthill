package app_test

import (
	"context"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
	"github.com/matteorosani/anthill/internal/testkit"
)

// notificationsWith loads a configuration whose notifications setting is
// set, and returns the filtering notifier and the fake behind it.
func notificationsWith(t *testing.T, setting map[string]bool) (*app.Notifications, *testkit.Notifier) {
	t.Helper()
	source := &testkit.ConfigSource{Config: ports.LocalConfig{
		File:   "config.yml",
		Global: domain.SettingValues{Notifications: setting},
	}}
	config := app.NewConfiguration(source)
	if _, err := config.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake := &testkit.Notifier{}
	return app.NewNotifications(config, fake), fake
}

// Every notification kind of queen-console is raised by default, and
// each one can be turned off on its own (spec queen-console,
// "Notifications").
func TestEveryNotificationKindIsRaisedAndCanBeTurnedOff(t *testing.T) {
	ctx := context.Background()
	for _, kind := range ports.NotificationKinds {
		t.Run(string(kind), func(t *testing.T) {
			on, onFake := notificationsWith(t, nil)
			if err := on.Notify(ctx, ports.Notification{Kind: kind, Title: "t"}); err != nil {
				t.Fatal(err)
			}
			if got := onFake.Kinds(); !slices.Equal(got, []ports.NotificationKind{kind}) {
				t.Errorf("on: raised %v, want [%s]", got, kind)
			}

			off, offFake := notificationsWith(t, map[string]bool{string(kind): false})
			if err := off.Notify(ctx, ports.Notification{Kind: kind, Title: "t"}); err != nil {
				t.Fatal(err)
			}
			if got := offFake.Kinds(); len(got) != 0 {
				t.Errorf("off: raised %v, want none", got)
			}
			// Turning one kind off leaves the others on.
			for _, other := range ports.NotificationKinds {
				if other != kind {
					_ = off.Notify(ctx, ports.Notification{Kind: other, Title: "t"})
				}
			}
			if got := len(offFake.Kinds()); got != len(ports.NotificationKinds)-1 {
				t.Errorf("off: %d other kinds raised, want %d", got, len(ports.NotificationKinds)-1)
			}
		})
	}
}

// A reload that turns a kind off applies to the next notification.
func TestNotificationsFollowReload(t *testing.T) {
	ctx := context.Background()
	source := &testkit.ConfigSource{Config: ports.LocalConfig{File: "config.yml"}}
	config := app.NewConfiguration(source)
	fake := &testkit.Notifier{}
	notifications := app.NewNotifications(config, fake)

	// Before the configuration is loaded, every kind is on.
	_ = notifications.Notify(ctx, ports.Notification{Kind: ports.NotifyClaimLost})
	if _, err := config.Load(ctx); err != nil {
		t.Fatal(err)
	}
	_ = notifications.Notify(ctx, ports.Notification{Kind: ports.NotifyClaimLost})
	source.Update(func(c *testkit.ConfigSource) {
		c.Config.Global.Notifications = map[string]bool{"claim-lost": false}
	})
	if _, err := config.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	_ = notifications.Notify(ctx, ports.Notification{Kind: ports.NotifyClaimLost})
	if got := len(fake.Kinds()); got != 2 {
		t.Errorf("%d notifications, want 2", got)
	}
}

// An unknown kind makes the global configuration invalid.
func TestUnknownNotificationKindPreventsStartup(t *testing.T) {
	source := &testkit.ConfigSource{Config: ports.LocalConfig{
		File:   "config.yml",
		Global: domain.SettingValues{Notifications: map[string]bool{"coffee-ready": false}},
	}}
	_, err := app.NewConfiguration(source).Load(context.Background())
	if got := messages(err); !slices.Equal(got, []string{`unknown notification kind "coffee-ready"`}) {
		t.Errorf("errors = %q", got)
	}
}
