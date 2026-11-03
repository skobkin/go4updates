//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests run only on native Windows runners (the Linux CI covers the
// same scenarios through the portable test suite). They exercise the
// move-aside replacement dance and the crash-window recovery that cannot be
// verified by cross-compilation alone.

func TestInstallWindowsReplacementDance(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	stagedPath := filepath.Join(dir, "staged")

	if err := os.WriteFile(target, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	if err := os.WriteFile(stagedPath, []byte("v2"), 0o600); err != nil {
		t.Fatalf("writing staged: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	backup, err := i.Install(t.Context(), stagedPath)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if backup != target+BackupSuffix {
		t.Errorf("backup = %q, want %q", backup, target+BackupSuffix)
	}

	gotV1, err := os.ReadFile(backup)
	if err != nil || string(gotV1) != "v1" {
		t.Errorf("backup content = %q (%v), want v1", gotV1, err)
	}

	gotV2, err := os.ReadFile(target)
	if err != nil || string(gotV2) != "v2" {
		t.Errorf("target content = %q (%v), want v2", gotV2, err)
	}
}

func TestInstallWindowsRefusesExistingBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	stagedPath := filepath.Join(dir, "staged")

	if err := os.WriteFile(target, []byte("v2"), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	backup := target + BackupSuffix

	if err := os.WriteFile(backup, []byte("previous-unconfirmed"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	if err := os.WriteFile(stagedPath, []byte("v3"), 0o600); err != nil {
		t.Fatalf("writing staged: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	// Without this guard the move-aside rename would silently overwrite the
	// unconfirmed backup: os.Rename on Windows replaces an existing
	// destination.
	if _, err := i.Install(t.Context(), stagedPath); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	gotTarget, err := os.ReadFile(target)
	if err != nil || string(gotTarget) != "v2" {
		t.Errorf("target content = %q (%v), want untouched v2", gotTarget, err)
	}

	gotBackup, err := os.ReadFile(backup)
	if err != nil || string(gotBackup) != "previous-unconfirmed" {
		t.Errorf("backup content = %q (%v), want untouched previous-unconfirmed", gotBackup, err)
	}
}

// Regression: when the staged rename and the restore both fail, the target
// is missing and only the backup holds the old binary. The documented
// contract promises the "target untouched" claim only for errors that do
// not wrap ErrReplaced, so these paths must wrap it and return the backup
// path; without that a caller cannot detect the displaced state.
func TestInstallWindowsRestoreFailureReportsDisplacedTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	stagedPath := filepath.Join(dir, "staged")

	if err := os.WriteFile(target, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	if err := os.WriteFile(stagedPath, []byte("v2"), 0o600); err != nil {
		t.Fatalf("writing staged: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	calls := 0

	i.renameFn = func(oldname, newname string) error {
		calls++

		// First call: the move-aside rename succeeds. Everything after it —
		// the staged replacement and the restore — fails.
		if calls == 1 {
			return os.Rename(oldname, newname)
		}

		return errors.New("injected rename failure")
	}

	backupPath, err := i.Install(t.Context(), stagedPath)
	if !errors.Is(err, ErrReplaced) {
		t.Fatalf("err = %v, want ErrReplaced for the displaced target", err)
	}

	if backupPath != target+BackupSuffix {
		t.Errorf("backup path = %q, want %q", backupPath, target+BackupSuffix)
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("target stat = %v, want missing after the failed restore", statErr)
	}

	got, err := os.ReadFile(target + BackupSuffix)
	if err != nil || string(got) != "v1" {
		t.Errorf("backup content = %q (%v), want the old binary v1", got, err)
	}
}

func TestInstallWindowsCancelAfterMoveAsideReportsDisplacedTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	stagedPath := filepath.Join(dir, "staged")

	if err := os.WriteFile(target, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	if err := os.WriteFile(stagedPath, []byte("v2"), 0o600); err != nil {
		t.Fatalf("writing staged: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	calls := 0

	i.renameFn = func(oldname, newname string) error {
		calls++

		// First call: complete the move-aside rename, then cancel, so the
		// cancellation lands between the two renames.
		if calls == 1 {
			if err := os.Rename(oldname, newname); err != nil {
				return err
			}

			cancel()

			return nil
		}

		return errors.New("injected restore failure")
	}

	backupPath, err := i.Install(ctx, stagedPath)
	if !errors.Is(err, ErrReplaced) {
		t.Fatalf("err = %v, want ErrReplaced for the displaced target", err)
	}

	if backupPath != target+BackupSuffix {
		t.Errorf("backup path = %q, want %q", backupPath, target+BackupSuffix)
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("target stat = %v, want missing after the failed restore", statErr)
	}
}

func TestCleanupWindowsRestoresMissingTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	backup := target + BackupSuffix

	if err := os.WriteFile(backup, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil || string(got) != "v1" {
		t.Fatalf("restored target = %q (%v), want v1", got, err)
	}
}

func TestCleanupWindowsRetiresBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app.exe")
	backup := target + BackupSuffix

	if err := os.WriteFile(target, []byte("v2"), 0o755); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	if err := os.WriteFile(backup, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("backup stat = %v, want removed", statErr)
	}
}
