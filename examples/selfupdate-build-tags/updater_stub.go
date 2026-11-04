//go:build no_selfupdate

package main

import (
	"context"
	"errors"

	updates "github.com/skobkin/go4updates"
)

// applySelfUpdate is the disabled wiring for distribution packaging: the
// selfupdate package is not imported at all, so the binary contains no
// self-update machinery — no code, no dependency in the build graph — and
// the application stays fully usable while a release is available. An
// explicit self-update request (--self-update) reports the feature as
// intentionally unavailable, naming the tag so the user knows to update
// through the package manager instead.
//
// Builds with -tags no_selfupdate compile this file instead of updater.go.
func applySelfUpdate(_ context.Context, _ updates.Result) error {
	return errors.New("self-update is disabled in this build (built with -tags no_selfupdate); install updates through your package manager")
}
