package selfupdate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// newPreflightUpdater builds an Updater against a fresh target in dir
// without any server wiring: CheckInstallability touches no network.
func newPreflightUpdater(t *testing.T, target string, opts ...Option) *Updater {
	t.Helper()

	base := []Option{WithTargetPath(target), WithAppName(fixtureAppName)}

	u, err := New(append(base, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return u
}

func TestCheckInstallabilityWritableTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	u := newPreflightUpdater(t, target)

	if err := u.CheckInstallability(t.Context()); err != nil {
		t.Fatalf("CheckInstallability: %v", err)
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

// A probe cleanup failure must fail the check instead of silently passing
// with leftover probe files.
func TestCheckInstallabilityCleanupFailureStillReports(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	u := newPreflightUpdater(t, target)

	removals := 0

	injected := errors.New("injected removal failure")

	// Removal call order in the probe: clear the rename destination, then
	// remove the renamed probe. Failing the second call leaves a leftover.
	u.installer.removeFn = func(name string) error {
		removals++

		if removals >= 2 {
			return injected
		}

		return os.Remove(name)
	}

	err := u.CheckInstallability(t.Context())
	if err == nil {
		t.Fatal("CheckInstallability passed with a failing probe removal")
	}

	if !errors.Is(err, injected) {
		t.Errorf("err = %v, want the removal failure in the chain", err)
	}
}

// When the probe itself fails and the cleanup also fails, the original
// diagnosis stays first and both failures stay in the chain.
func TestCheckInstallabilityCleanupFailureJoinedWithProbeFailure(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	u := newPreflightUpdater(t, target)

	renameErr := errors.New("injected rename failure")
	removalErr := errors.New("injected removal failure")

	removals := 0

	u.installer.renameFn = func(_, _ string) error {
		return renameErr
	}

	// Removal call order: clear the rename destination (must succeed so
	// the probe reaches the failing rename), then the failure cleanup.
	u.installer.removeFn = func(name string) error {
		removals++

		if removals == 1 {
			return os.Remove(name)
		}

		return removalErr
	}

	err := u.CheckInstallability(t.Context())
	if err == nil {
		t.Fatal("CheckInstallability passed with a failing rename")
	}

	if !errors.Is(err, renameErr) {
		t.Errorf("err = %v, want the original rename failure in the chain", err)
	}

	if !errors.Is(err, removalErr) {
		t.Errorf("err = %v, want the cleanup failure joined into the chain", err)
	}
}

// A failing rename must be reported with both probe files cleaned up.
func TestCheckInstallabilityRenameProbeFailure(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	u := newPreflightUpdater(t, target)

	injected := errors.New("injected rename failure")

	u.installer.renameFn = func(_, _ string) error {
		return injected
	}

	err := u.CheckInstallability(t.Context())
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("err = %v, want the injected rename failure", err)
	}

	assertNoStagingLeftovers(t, dir, fixtureAppName)
}

// Create failures wrap the underlying cause so errors.Is finds it — the
// CI-safe substitute for chmod-based permission tests, which do not bind
// for root.
func TestCheckInstallabilityCreateProbeFailure(t *testing.T) {
	for _, cause := range []error{fs.ErrPermission, syscall.EROFS} {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing target: %v", err)
		}

		u := newPreflightUpdater(t, target)

		u.installer.createTempFn = func(_, _ string) (*os.File, error) {
			return nil, &fs.PathError{Op: "create", Path: dir, Err: cause}
		}

		err := u.CheckInstallability(t.Context())
		if err == nil || !errors.Is(err, cause) {
			t.Errorf("err = %v, want %v in the chain", err, cause)
		}
	}
}

func TestCheckInstallabilityBackupExists(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	backup := target + BackupSuffix

	if err := os.WriteFile(backup, []byte("ancient"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	u := newPreflightUpdater(t, target)

	if err := u.CheckInstallability(t.Context()); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	// The Windows crash shape — target missing, backup present — is
	// reported as the backup it is, before the target check could pass.
	if err := os.Remove(target); err != nil {
		t.Fatalf("removing target: %v", err)
	}

	if err := u.CheckInstallability(t.Context()); !errors.Is(err, ErrBackupExists) {
		t.Errorf("err = %v for the displaced-target shape, want ErrBackupExists", err)
	}

	if got := readFileForTest(t, backup); got != "ancient" {
		t.Errorf("backup content = %q, want untouched %q", got, "ancient")
	}
}

func TestCheckInstallabilityInvalidTargets(t *testing.T) {
	t.Run("directory target", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}

		u := newPreflightUpdater(t, target)

		if err := u.CheckInstallability(t.Context()); !errors.Is(err, ErrTargetNotRegular) {
			t.Fatalf("err = %v, want ErrTargetNotRegular", err)
		}
	})

	t.Run("symlink target resolves before the check", func(t *testing.T) {
		dir := t.TempDir()

		real := filepath.Join(dir, "real")

		if err := os.WriteFile(real, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing real file: %v", err)
		}

		target := filepath.Join(dir, fixtureAppName)

		if err := os.Symlink(real, target); err != nil {
			t.Fatalf("symlinking: %v", err)
		}

		// New resolves symlinks in the target path, so the check sees the
		// regular file behind the link and passes.
		u := newPreflightUpdater(t, target)

		if err := u.CheckInstallability(t.Context()); err != nil {
			t.Fatalf("err = %v, want nil for a symlink resolving to a regular file", err)
		}
	})

	t.Run("no execute bits without a forced mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("execute bits are a Unix concept")
		}

		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.WriteFile(target, []byte(oldPayload), 0o644); err != nil {
			t.Fatalf("writing target: %v", err)
		}

		u := newPreflightUpdater(t, target)

		if err := u.CheckInstallability(t.Context()); !errors.Is(err, ErrTargetNotExecutable) {
			t.Fatalf("err = %v, want ErrTargetNotExecutable", err)
		}

		// An explicit WithFileMode bypasses the inheritance rule, exactly
		// like the installer does.
		u = newPreflightUpdater(t, target, WithFileMode(0o755))

		if err := u.CheckInstallability(t.Context()); err != nil {
			t.Errorf("err = %v with WithFileMode, want nil", err)
		}
	})

	t.Run("missing target passes", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		u := newPreflightUpdater(t, target)

		if err := u.CheckInstallability(t.Context()); err != nil {
			t.Fatalf("err = %v, want nil for a fresh target", err)
		}
	})
}

// The check must never modify, rename, truncate, or remove the target or
// the backup.
func TestCheckInstallabilityNonDestructive(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	backup := target + BackupSuffix

	if err := os.WriteFile(backup, []byte("ancient"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	u := newPreflightUpdater(t, target)

	if err := u.CheckInstallability(t.Context()); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("target survived: %v", err)
	}

	if got := readFileForTest(t, target); got != oldPayload || info.Mode().Perm() != 0o755 {
		t.Errorf("target changed: content %q mode %v", got, info.Mode().Perm())
	}

	if got := readFileForTest(t, backup); got != "ancient" {
		t.Errorf("backup changed: %q", got)
	}
}

func TestCheckInstallabilityCanceledContext(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, fixtureAppName)

	if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	u := newPreflightUpdater(t, target)

	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	if err := u.CheckInstallability(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The check coordinates with the lifecycle: while an Apply runs, it waits
// instead of racing the pipeline's staging files — and reports the
// cancellation when its own context is canceled while waiting.
func TestCheckInstallabilityWaitsForLifecycle(t *testing.T) {
	t.Run("waits for a running Apply, then reports the state it left", func(t *testing.T) {
		u, dir, _, started, rel, result := blockedReleaseFixture(t)

		applyDone := make(chan error, 1)

		go func() {
			_, err := u.Apply(t.Context(), result)

			applyDone <- err
		}()

		<-started

		checkDone := make(chan error, 1)

		go func() {
			checkDone <- u.CheckInstallability(t.Context())
		}()

		// Finish the update. The check must have been waiting for the
		// pipeline: its result therefore reflects the state after the
		// update — a retained backup awaiting confirmation — and not the
		// backup-free state of the mid-download world.
		close(rel)

		if err := <-applyDone; err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if err := <-checkDone; !errors.Is(err, ErrBackupExists) {
			t.Errorf("err = %v, want ErrBackupExists from a check that waited for the Apply", err)
		}

		assertNoStagingLeftovers(t, dir, fixtureAppName)
	})

	t.Run("canceled while waiting for the lock", func(t *testing.T) {
		u, _, _, started, rel, result := blockedReleaseFixture(t)

		applyDone := make(chan error, 1)

		go func() {
			_, err := u.Apply(t.Context(), result)

			applyDone <- err
		}()

		<-started

		ctx, cancel := context.WithCancel(context.Background())

		cancel()

		err := u.CheckInstallability(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled while Apply holds the lock", err)
		}

		close(rel)

		if err := <-applyDone; err != nil {
			t.Fatalf("Apply: %v", err)
		}
	})

	t.Run("waits for a running Cleanup, then passes on the swept state", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, fixtureAppName)

		if err := os.WriteFile(target, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing target: %v", err)
		}

		// A retired backup gives Cleanup real work. Its removal parks on a
		// channel, so Cleanup deterministically holds the lifecycle while
		// the check is called.
		backup := target + BackupSuffix

		if err := os.WriteFile(backup, []byte(oldPayload), 0o755); err != nil {
			t.Fatalf("writing backup: %v", err)
		}

		u := newPreflightUpdater(t, target)

		sweeping := make(chan struct{})
		release := make(chan struct{})

		u.installer.removeFn = func(name string) error {
			if name != backup {
				return os.Remove(name)
			}

			sweeping <- struct{}{}

			<-release

			return os.Remove(name)
		}

		cleanupDone := make(chan error, 1)

		go func() {
			cleanupDone <- u.Cleanup(t.Context())
		}()

		<-sweeping

		checkDone := make(chan error, 1)

		go func() {
			checkDone <- u.CheckInstallability(t.Context())
		}()

		close(release)

		if err := <-cleanupDone; err != nil {
			t.Fatalf("Cleanup: %v", err)
		}

		if err := <-checkDone; err != nil {
			t.Errorf("err = %v after the Cleanup retired the backup, want nil", err)
		}
	})
}
