package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNoSelfupdateVariantCompiles builds the -tags no_selfupdate variant:
// the regular build, vet, and lint compile only the default wiring, so
// without this check the stub would be free to rot. The binary lands in a
// temporary directory, never inside the repository.
func TestNoSelfupdateVariantCompiles(t *testing.T) {
	out := filepath.Join(t.TempDir(), "example-app")

	// Constant arguments only: the go tool is resolved from PATH.
	cmd := exec.Command("go", "build", "-tags", "no_selfupdate", "-o", out, ".")
	cmd.Dir = "."

	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the no_selfupdate variant: %v\n%s", err, combined)
	}

	if _, err := os.Stat(out); err != nil {
		t.Fatalf("no_selfupdate binary was not produced: %v", err)
	}
}
