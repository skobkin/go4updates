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
// the feature reports itself as intentionally unavailable.
//
// Builds with -tags no_selfupdate compile this file instead of updater.go.
func applySelfUpdate(_ context.Context, result updates.Result) error {
	if result.Status != updates.StatusUpdateAvailable {
		return nil
	}

	return errors.New("self-update is disabled in this build (built with -tags no_selfupdate)")
}
