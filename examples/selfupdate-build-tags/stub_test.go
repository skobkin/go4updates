//go:build no_selfupdate

package main

import (
	"context"
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
)

// An explicit self-update request in a disabled build must fail with an
// informative error naming the build tag — never silently succeed and
// never affect normal startup, which never calls the wiring at all.
func TestDisabledStubReportsInformativeError(t *testing.T) {
	err := applySelfUpdate(context.Background(), updates.Result{
		CurrentVersion: "1.0.0",
		Status:         updates.StatusUpdateAvailable,
	})
	if err == nil {
		t.Fatal("applySelfUpdate succeeded in a no_selfupdate build")
	}

	if !strings.Contains(err.Error(), "no_selfupdate") {
		t.Fatalf("error does not name the build tag: %v", err)
	}
}
