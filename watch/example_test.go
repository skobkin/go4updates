package watch_test

import (
	"context"
	"log/slog"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/source/forgejo"
	"github.com/skobkin/go4updates/watch"
)

// A watcher checks each target immediately and again one interval after each
// completed attempt. Events arrive on a channel the application owns, so the
// watcher never touches GUI threads or application scheduling.
func ExampleWatcher() {
	src, err := forgejo.New(
		"https://git.example.org",
		"team/desktop-app",
	)
	if err != nil {
		slog.Error("invalid source configuration", "error", err)

		return
	}

	checker := updates.NewChecker(updates.CheckerOptions{})

	w := watch.New(checker, watch.Options{
		Interval: 12 * time.Hour,
		Timeout:  15 * time.Second,
	})

	err = w.Add("application", updates.Target{
		CurrentVersion: "v1.2.3",
		Source:         src,
		Fetch: updates.FetchOptions{
			Limit: 15,
		},
	})
	if err != nil {
		slog.Error("cannot register target", "error", err)

		return
	}

	events, unsubscribe := w.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for event := range events {
			// Bridge the generic event into the application's own
			// event bus, notification service, GUI thread, etc.
			bridgeToApplication(event)
		}
	}()

	go func() {
		if err := w.Run(ctx); err != nil {
			slog.Error("update watcher stopped", "error", err)
		}
	}()
}

// bridgeToApplication stands in for the application's own event handling.
func bridgeToApplication(watch.Event) {}
