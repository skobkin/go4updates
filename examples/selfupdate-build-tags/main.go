// Command example-app demonstrates the recommended build-tag pattern for
// making self-update optional at build time. The application code is
// identical in both variants; only the wiring file changes:
//
//	go build ./examples/selfupdate-build-tags                      # self-update wired in
//	go build -tags no_selfupdate ./examples/selfupdate-build-tags  # excluded entirely
//
// Distribution packaging (package managers, Flatpak) builds with
// -tags no_selfupdate so the self-update machinery is absent from the
// binary; standalone CI builds omit the tag and keep the feature. See
// updater.go (enabled wiring) and updater_stub.go (disabled wiring).
package main

import (
	"context"
	"log"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/source/github"
)

// appName is the release asset's application name; the updater matches
// assets against it with the default platform policy.
const appName = "example-app"

func main() {
	source, err := github.New("example-org/example-app")
	if err != nil {
		log.Fatal(err)
	}

	checker := updates.NewChecker(updates.CheckerOptions{})

	result, err := checker.Check(context.Background(), updates.Target{
		CurrentVersion: "1.0.0",
		Source:         source,
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := applySelfUpdate(context.Background(), result); err != nil {
		log.Fatal(err)
	}
}
