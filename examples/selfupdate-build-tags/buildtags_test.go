package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestNoSelfupdateKeepsPackageOutOfBuildGraph asserts the documented
// guarantee behind the tag, not just successful linkage: the no_selfupdate
// build must not contain the selfupdate package at all. A refactor that
// gives code shared by both variants a selfupdate import would otherwise
// ship the whole machinery — and its dependency graph — while the compile
// test above stays green. The default variant is asserted to still contain
// the package, proving the check can actually fail.
func TestNoSelfupdateKeepsPackageOutOfBuildGraph(t *testing.T) {
	// Constant arguments only: the go tool is resolved from PATH.
	without, err := exec.Command("go", "list", "-deps", "-tags", "no_selfupdate", ".").Output()
	if err != nil {
		t.Fatalf("listing the no_selfupdate build graph: %v\n%s", err, without)
	}

	if strings.Contains(string(without), "go4updates/selfupdate") {
		t.Fatalf("the no_selfupdate build graph contains the selfupdate package:\n%s", without)
	}

	with, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("listing the default build graph: %v\n%s", err, with)
	}

	if !strings.Contains(string(with), "go4updates/selfupdate") {
		t.Fatalf("sanity: the default build graph lost the selfupdate package, so the exclusion check above could never fire:\n%s", with)
	}
}
