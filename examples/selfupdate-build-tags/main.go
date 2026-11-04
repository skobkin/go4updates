// Command example-app demonstrates the recommended build-tag pattern for
// making self-update optional at build time. The application code is
// identical in both variants; only the wiring file changes:
//
//	go build ./examples/selfupdate-build-tags                      # self-update wired in
//	go build -tags no_selfupdate ./examples/selfupdate-build-tags  # excluded entirely
//
// Self-update is never a startup side effect: the application only checks
// for a release at startup (informational) and installs one when the user
// explicitly passes --self-update — a package-managed binary must not
// replace itself just because a release exists. Distribution packaging
// builds with -tags no_selfupdate so the self-update machinery is absent
// from the binary, and the flag stays usable there: it reports the feature
// as intentionally unavailable instead of failing the application. See
// updater.go (enabled wiring) and updater_stub.go (disabled wiring).
package main

import (
	"context"
	"log"
	"os"
	"slices"

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

	if selfUpdateRequested(os.Args[1:]) {
		if err := applySelfUpdate(context.Background(), result); err != nil {
			log.Fatal(err)
		}

		return
	}

	if result.Status == updates.StatusUpdateAvailable && result.Latest != nil {
		log.Printf("update to %s available; run with --self-update to install", result.Latest.Version)
	}
}

// selfUpdateRequested reports whether the user explicitly asked for a
// self-update on the command line. It takes the arguments instead of
// reading os.Args so tests can exercise it directly.
func selfUpdateRequested(args []string) bool {
	return slices.Contains(args, "--self-update")
}
