//go:build windows

package selfupdate

import (
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

	if err := i.Cleanup(); err != nil {
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

	if err := i.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("backup stat = %v, want removed", statErr)
	}
}
