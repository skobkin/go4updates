package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
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

// Installing is an explicit application decision, so the --self-update
// flag scan is pinned: normal startup (no flag, unrelated flags) must
// never trigger the wiring.
func TestSelfUpdateRequested(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--verbose"}, false},
		{[]string{"--self-updates"}, false},
		{[]string{"--self-update"}, true},
		{[]string{"serve", "--self-update"}, true},
	} {
		if got := selfUpdateRequested(tc.args); got != tc.want {
			t.Errorf("selfUpdateRequested(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// The enabled wiring must stay a no-op on an up-to-date result: the status
// gate runs before any updater work, so an explicit request on a current
// build reports success without network or filesystem activity.
func TestEnabledWiringUpToDateIsNoop(t *testing.T) {
	err := applySelfUpdate(t.Context(), updates.Result{
		CurrentVersion: "1.0.0",
		Status:         updates.StatusUpToDate,
	})
	if err != nil {
		t.Fatalf("applySelfUpdate on an up-to-date result: %v", err)
	}
}

// The disabled variant's wiring is compiled out of the default test
// binary, so its informative-error contract is exercised through a tagged
// test run of the same package (see stub_test.go) — keeping both the
// enabled and the disabled behavior covered by the ordinary suite.
func TestNoSelfupdateVariantBehavior(t *testing.T) {
	// Constant arguments only: the go tool is resolved from PATH.
	cmd := exec.Command("go", "test", "-tags", "no_selfupdate", "-run", "^TestDisabledStubReportsInformativeError$", ".")
	cmd.Dir = "."

	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the no_selfupdate behavior tests: %v\n%s", err, combined)
	}
}
