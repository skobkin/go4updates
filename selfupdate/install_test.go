package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// writeTarget creates a fake installed binary.
func writeTarget(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func newTestInstaller(t *testing.T, target string) *Installer {
	t.Helper()

	i, err := NewInstaller(WithTargetPath(target))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	return i
}

func stagedFile(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "app.go4updates.new-staged")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing staged file: %v", err)
	}

	return path
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	return string(data)
}

func fileModeForTest(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	return info.Mode().Perm()
}

func TestInstallFreshTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	staged := stagedFile(t, dir, "v2")

	i := newTestInstaller(t, target)

	backup, err := i.Install(t.Context(), staged)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if backup != "" {
		t.Errorf("backup = %q, want empty for a fresh target", backup)
	}

	if got := readFileForTest(t, target); got != "v2" {
		t.Errorf("target content = %q, want v2", got)
	}

	if mode := fileModeForTest(t, target); mode != 0o755 {
		t.Errorf("fresh target mode = %v, want 0755", mode)
	}
}

func TestInstallPreservesTargetMode(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o700)

	i := newTestInstaller(t, target)

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if mode := fileModeForTest(t, target); mode != 0o700 {
		t.Errorf("replaced target mode = %v, want preserved 0700", mode)
	}
}

func TestInstallWithFileMode(t *testing.T) {
	dir := t.TempDir()

	t.Run("read-only executable modes install as configured", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o555, 0o500, 0o755} {
			target := filepath.Join(dir, fmt.Sprintf("app-mode-%o", mode))

			i, err := NewInstaller(WithTargetPath(target), WithFileMode(mode))
			if err != nil {
				t.Fatalf("NewInstaller(WithFileMode(%v)): %v", mode, err)
			}

			if _, err := i.Install(t.Context(), stagedFile(t, dir, "v1")); err != nil {
				t.Fatalf("Install with mode %v: %v", mode, err)
			}

			if got := fileModeForTest(t, target); got != mode {
				t.Errorf("mode = %v, want %v", got, mode)
			}
		}
	})

	t.Run("explicit mode overrides preservation", func(t *testing.T) {
		target := filepath.Join(dir, "app-over")
		writeTarget(t, target, "v1", 0o700)

		i, err := NewInstaller(WithTargetPath(target), WithFileMode(0o755))
		if err != nil {
			t.Fatalf("NewInstaller: %v", err)
		}

		if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err != nil {
			t.Fatalf("Install: %v", err)
		}

		if mode := fileModeForTest(t, target); mode != 0o755 {
			t.Errorf("mode = %v, want 0755", mode)
		}
	})

	t.Run("modes without an execute bit are rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows file names, not permission bits, decide executability")
		}

		for _, mode := range []fs.FileMode{0o640, 0o600, 0o644, 0o400} {
			target := filepath.Join(dir, fmt.Sprintf("app-rejected-%o", mode))

			_, err := NewInstaller(WithTargetPath(target), WithFileMode(mode))
			if err == nil {
				t.Errorf("WithFileMode(%v) should be rejected: it installs a binary nobody can execute", mode)

				continue
			}

			if !strings.Contains(err.Error(), "execute") {
				t.Errorf("WithFileMode(%v) error = %v, want it to mention the execute bit", mode, err)
			}
		}
	})
}

// Installing over a non-executable target inherits its mode by default, so
// the replaced binary would not be runnable either. The inherited mode is
// validated like an explicit one: the install fails with the target
// untouched and points at the remedy.
func TestInstallRejectsNonExecutableInheritedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file names, not permission bits, decide executability")
	}

	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o644)

	i := newTestInstaller(t, target)

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err == nil {
		t.Fatal("Install should fail when the inherited target mode has no execute bit")
	} else if !strings.Contains(err.Error(), "execute") {
		t.Errorf("Install error = %v, want it to mention the execute bit", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified by the failed install: %q", got)
	}

	if mode := fileModeForTest(t, target); mode != 0o644 {
		t.Errorf("target mode = %v, want the untouched 0644", mode)
	}

	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("leftover %q survived the failed install", e)
		}
	}
}

// Regression: the zero mode slipped through validation, and the installed
// binary ended up non-executable for everyone — with the mode also inherited
// by every following install through the target-preservation default. The
// option rejects it at construction instead.
func TestWithFileModeRejectsZeroMode(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "app")

	for _, mode := range []fs.FileMode{0, 0o000} {
		_, err := NewInstaller(WithTargetPath(abs), WithFileMode(mode))
		if err == nil {
			t.Errorf("WithFileMode(%v) should be rejected: it installs a mode-0000 binary", mode)

			continue
		}

		if !strings.Contains(err.Error(), "non-zero") {
			t.Errorf("WithFileMode(%v) error = %v, want it to say non-zero", mode, err)
		}
	}
}

func TestInstallBackupIsHardlinkOfOldBinary(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	preStat, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}

	i := newTestInstaller(t, target)

	backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if backup != target+BackupSuffix {
		t.Fatalf("backup = %q, want %q", backup, target+BackupSuffix)
	}

	postStat, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}

	if !os.SameFile(preStat, postStat) {
		t.Error("backup must be the same inode as the pre-install target (hardlink)")
	}

	if got := readFileForTest(t, target); got != "v2" {
		t.Errorf("target content = %q, want v2", got)
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want v1", got)
	}
}

// The attempt-backup removal after a failed replacement is best-effort:
// when it fails too, the error must say so and the backup must survive,
// even though the retry will then be blocked by ErrBackupExists.
func TestInstallFailedReplacementRemovalFailureIsReported(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	backup := target + BackupSuffix

	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.renameFn = func(_, _ string) error {
		return errors.New("injected rename failure")
	}

	i.removeFn = func(string) error {
		return errors.New("injected remove failure")
	}

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err == nil {
		t.Fatal("Install should fail on the injected rename failure")
	}

	if !strings.Contains(err.Error(), "removing the attempt backup") {
		t.Errorf("error should report the failed attempt-backup removal, got: %v", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target content = %q, want untouched v1", got)
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want v1 (best-effort removal failed)", got)
	}
}

func TestInstallRefusesBackupWhenTargetMissing(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	backup := target + BackupSuffix

	// Post-crash-window state: the target is missing while an unconfirmed
	// backup remains. Installing must refuse instead of stranding the
	// backup; the documented resolution is Cleanup (which restores the
	// target) and a retry.
	writeTarget(t, backup, "crash-window-residue", 0o755)

	i := newTestInstaller(t, target)

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("a refused install must not create the target")
	}

	if got := readFileForTest(t, backup); got != "crash-window-residue" {
		t.Errorf("backup content = %q, want untouched crash-window-residue", got)
	}
}

func TestInstallRefusesExistingBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	backup := target + BackupSuffix
	writeTarget(t, backup, "previous-unconfirmed", 0o755)

	i := newTestInstaller(t, target)

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified despite ErrBackupExists: %q", got)
	}

	if got := readFileForTest(t, backup); got != "previous-unconfirmed" {
		t.Errorf("pre-existing backup was modified: %q", got)
	}
}

func TestInstallLinkRaceIsNotCopyFallback(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	copyCalled := false

	i.linkFn = func(oldname, newname string) error {
		return fs.ErrExist
	}

	i.copyFn = func(dst io.Writer, srcPath string) error {
		copyCalled = true

		return nil
	}

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists for the EEXIST race", err)
	}

	if copyCalled {
		t.Error("EEXIST must not trigger the copy fallback")
	}
}

func TestInstallInterruptedCopyFallbackLeavesNoBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.linkFn = func(oldname, newname string) error {
		return errors.New("enosys: hardlinks unsupported")
	}

	i.copyFn = func(dst io.Writer, srcPath string) error {
		// Interrupted copy: partial bytes, then failure.
		_, _ = io.WriteString(dst, "part")

		return errors.New("interrupted")
	}

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err == nil {
		t.Fatal("Install should fail when the backup copy fails")
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr == nil {
		t.Error("no file may exist at the backup name after an interrupted copy")
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified: %q", got)
	}

	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("backup staging leftover %q was not removed", e)
		}
	}
}

func TestInstallCopyFallbackPublishesCompleteBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.linkFn = func(oldname, newname string) error {
		return errors.New("enosys: hardlinks unsupported")
	}

	backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if backup == "" {
		t.Fatal("backup path is empty after a successful copy fallback")
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want v1", got)
	}

	if mode := fileModeForTest(t, backup); mode != 0o755 {
		t.Errorf("backup mode = %v, want the preserved 0755", mode)
	}

	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("backup staging leftover %q was not removed", e)
		}
	}
}

func TestInstallCopyFallbackFailureFailsInstall(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.linkFn = func(oldname, newname string) error {
		return errors.New("enosys: hardlinks unsupported")
	}

	i.copyFn = func(dst io.Writer, srcPath string) error {
		return errors.New("copy failed")
	}

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err == nil {
		t.Fatal("Install should fail when neither link nor copy is possible")
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified: %q", got)
	}
}

// The copy fallback must publish the backup without clobbering: the staging
// file is linked onto the backup name — an operation that fails with EEXIST
// instead of replacing whatever appeared there — and the rename, which
// would silently replace, is reserved for filesystems without hardlinks.
func TestInstallCopyFallbackPublishesWithoutClobbering(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.linkFn = func(oldname, newname string) error {
		if oldname == target {
			// Hardlinks to the target are refused; links to the staging
			// file the fallback creates are the no-clobber publication.
			return errors.New("eperm: link refused for the target")
		}

		return os.Link(oldname, newname)
	}

	var renamed []string

	i.renameFn = func(oldname, newname string) error {
		renamed = append(renamed, oldname)

		return os.Rename(oldname, newname)
	}

	backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want v1", got)
	}

	for _, oldname := range renamed {
		if strings.Contains(oldname, ".go4updates.tmp") {
			t.Errorf("backup was published with a clobbering rename of %q", oldname)
		}
	}

	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("backup staging leftover %q was not removed", e)
		}
	}
}

// A backup appearing between the existence check and the publication must
// fail the install with ErrBackupExists, never be replaced: the EEXIST the
// no-clobber link reports is the cross-process race surfacing safely.
func TestInstallCopyFallbackLinkRaceKeepsBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.linkFn = func(oldname, _ string) error {
		if oldname == target {
			return errors.New("enosys: hardlinks unsupported")
		}

		// Another process published a backup while this one was copying.
		return fs.ErrExist
	}

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists for the publication race", err)
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr == nil {
		t.Error("a backup must not exist after the losing side of the race gave up")
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified: %q", got)
	}

	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("backup staging leftover %q was not removed", e)
		}
	}
}

func TestInstallRenameFailureRemovesOwnBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	i.renameFn = func(oldname, newname string) error {
		return errors.New("rename refused")
	}

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err == nil {
		t.Fatal("Install should fail when the rename fails")
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr == nil {
		t.Error("the backup of a failed attempt should have been removed")
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified by the failed attempt: %q", got)
	}

	// A retry without the fault must not be blocked.
	i.renameFn = os.Rename

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err != nil {
		t.Fatalf("retry Install: %v", err)
	}

	if got := readFileForTest(t, target); got != "v2" {
		t.Errorf("retry did not install: %q", got)
	}
}

func TestInstallPreexistingBackupSurvivesFailure(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	backup := target + BackupSuffix
	writeTarget(t, backup, "precious", 0o755)

	i := newTestInstaller(t, target)

	// Install refuses up front (ErrBackupExists); nothing may be removed.
	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	if got := readFileForTest(t, backup); got != "precious" {
		t.Errorf("pre-existing backup was touched: %q", got)
	}
}

func TestInstallPostReplacementSyncFailureReportsReplaced(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	syncCalled := 0

	i.syncDirFn = func(dir string) error {
		syncCalled++

		// Fail only after the replacement.
		if syncCalled > 1 {
			return errors.New("sync failed")
		}

		return nil
	}

	backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err == nil {
		t.Fatal("Install should report the post-replacement sync failure")
	}

	if !errors.Is(err, ErrReplaced) {
		t.Fatalf("err = %v, want ErrReplaced in the chain", err)
	}

	if backup == "" {
		t.Error("backup path should be reported together with ErrReplaced")
	}

	if got := readFileForTest(t, target); got != "v2" {
		t.Errorf("replacement did not stand: %q", got)
	}

	if _, statErr := os.Lstat(backup); statErr != nil {
		t.Error("backup should survive a post-replacement failure")
	}
}

// Filesystems and platforms that cannot sync a directory at all report
// EINVAL/ENOTSUP-class errors; the documented best effort must tolerate
// them instead of failing a completed install. Genuine I/O failures are
// still reported, wrapped in ErrReplaced so the caller knows the
// replacement took effect despite the error.
func TestInstallToleratesUnsupportedDirSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("syncDir is a no-op on Windows; the errno classification is a Unix path")
	}

	cases := []struct {
		name string
		err  error
	}{
		{name: "EINVAL", err: syscall.EINVAL},
		{name: "ENOTSUP", err: syscall.ENOTSUP},
		{name: "wrapped EOPNOTSUPP", err: fmt.Errorf("sync directory: %w", syscall.EOPNOTSUPP)},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			target := filepath.Join(dir, "app")
			writeTarget(t, target, "v1", 0o755)

			i := newTestInstaller(t, target)
			i.syncDirFn = func(string) error { return tt.err }

			backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
			if err != nil {
				t.Fatalf("Install must tolerate an unsupported directory sync, got: %v", err)
			}

			if backup == "" {
				t.Error("backup path is empty after a successful install")
			}

			if got := readFileForTest(t, target); got != "v2" {
				t.Errorf("target content = %q, want v2", got)
			}
		})
	}

	t.Run("genuine sync failure still reports ErrReplaced", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, "app")
		writeTarget(t, target, "v1", 0o755)

		i := newTestInstaller(t, target)
		i.syncDirFn = func(string) error { return errors.New("input/output error") }

		backup, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
		if !errors.Is(err, ErrReplaced) {
			t.Fatalf("err = %v, want ErrReplaced for a real directory-sync failure", err)
		}

		if backup == "" {
			t.Error("the replacement took effect; the backup path must be reported")
		}

		if got := readFileForTest(t, target); got != "v2" {
			t.Errorf("target content = %q, want v2: the replacement already happened", got)
		}
	})
}

func TestInstallCanceledContext(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := i.Install(ctx, stagedFile(t, dir, "v2"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified: %q", got)
	}
}

// Regression: on the cancellation path after the backup publication, the
// removal error of the now-redundant attempt backup was discarded. A failed
// removal left the backup in place, silently blocking every retry with
// ErrBackupExists. The failure is folded into the cancellation error, the
// way the rename-failure path already does.
func TestInstallCancelAfterBackupReportsFailedRemoval(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// First syncDir call: durability of the backup publication, right
	// before the cancellation check. Cancel there.
	i.syncDirFn = func(string) error {
		cancel()

		return nil
	}

	i.removeFn = func(string) error {
		return errors.New("injected removal failure")
	}

	_, err := i.Install(ctx, stagedFile(t, dir, "v2"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if !strings.Contains(err.Error(), "removing the attempt backup") {
		t.Fatalf("err = %v, want it to report the failed backup removal", err)
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr != nil {
		t.Errorf("backup stat = %v, want the backup retained after the failed removal", statErr)
	}
}

// errInjectedReplace and errInjectedRestore are distinct sentinels so the
// Windows-sequence tests can prove that every failure of a displaced-target
// install stays individually identifiable with errors.Is.
var (
	errInjectedReplace = errors.New("injected: staged rename failed")
	errInjectedRestore = errors.New("injected: backup restore failed")
)

// newWindowsSequenceInstaller forces the Windows two-rename replacement on
// any platform, so the displaced-target restore paths are exercised by the
// regular test run instead of only on native Windows runners.
func newWindowsSequenceInstaller(t *testing.T, target string) *Installer {
	t.Helper()

	i := newTestInstaller(t, target)
	i.windowsSequence = true

	return i
}

// Regression: when the Windows replacement rename failed and the backup
// restoration failed too, the returned error named only the restoration
// failure — the original installation error was dropped, so callers could
// neither log why the update failed nor classify it. All three signals are
// preserved: ErrReplaced for the displaced target, the original failure,
// and the restoration failure, each reachable with errors.Is.
func TestInstallWindowsRestoreFailurePreservesEveryError(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	backup := target + BackupSuffix

	writeTarget(t, target, "v1", 0o755)

	i := newWindowsSequenceInstaller(t, target)

	i.renameFn = func(oldname, newname string) error {
		switch {
		case oldname == target && newname == backup:
			// Moving the old binary aside succeeds.
			return os.Rename(oldname, newname)
		case oldname == backup && newname == target:
			// The restoration fails as well.
			return errInjectedRestore
		default:
			// staged -> target fails.
			return errInjectedReplace
		}
	}

	backupPath, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err == nil {
		t.Fatal("Install should fail on the injected rename failures")
	}

	if !errors.Is(err, ErrReplaced) {
		t.Errorf("err = %v, want ErrReplaced for the displaced target", err)
	}

	if !errors.Is(err, errInjectedReplace) {
		t.Errorf("err = %v, want the original installation failure in the chain", err)
	}

	if !errors.Is(err, errInjectedRestore) {
		t.Errorf("err = %v, want the restoration failure in the chain", err)
	}

	if backupPath != backup {
		t.Errorf("backup path = %q, want %q so the caller can recover", backupPath, backup)
	}

	// Displaced state: the target is missing, the backup holds the old
	// binary.
	if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("target stat = %v, want the displaced (missing) target", statErr)
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want the preserved v1", got)
	}

	// The documented recovery must work from the reported state.
	i.renameFn = os.Rename

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup after the displaced state: %v", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target content after recovery = %q, want restored v1", got)
	}
}

// deadlineContext wraps a parent context and starts reporting
// context.DeadlineExceeded when expire is called: a deterministic stand-in
// for a deadline lapsing in the middle of the replacement sequence, where
// Install's entry check must still see a live context. (Canceling a live
// deadline context would report context.Canceled, not the deadline error.)
type deadlineContext struct {
	context.Context

	mu      sync.Mutex
	expired bool
}

// expire makes the context report context.DeadlineExceeded from now on.
func (c *deadlineContext) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.expired = true
}

// Err implements context.Context.
func (c *deadlineContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.expired {
		return context.DeadlineExceeded
	}

	return c.Context.Err()
}

// Regression: the Windows cancellation path — the sequence notices the
// lapsed context right after moving the old binary aside — dropped the
// original context error when the restoration failed, so callers could not
// tell a canceled update from a failed one. The context error stays in the
// chain next to ErrReplaced and the restoration failure.
func TestInstallWindowsCanceledRestorePreservesContextError(t *testing.T) {
	cases := []struct {
		name    string
		want    error
		wrapCtx func(t *testing.T) (context.Context, func())
	}{
		{
			name: "canceled",
			want: context.Canceled,
			wrapCtx: func(t *testing.T) (context.Context, func()) {
				ctx, cancel := context.WithCancel(t.Context())

				return ctx, cancel
			},
		},
		{
			name: "deadline exceeded",
			want: context.DeadlineExceeded,
			wrapCtx: func(t *testing.T) (context.Context, func()) {
				dc := &deadlineContext{Context: t.Context()}

				return dc, dc.expire
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			target := filepath.Join(dir, "app")
			backup := target + BackupSuffix

			writeTarget(t, target, "v1", 0o755)

			i := newWindowsSequenceInstaller(t, target)

			ctx, trigger := tt.wrapCtx(t)

			// The sequence's cancellation checkpoint sits right after the
			// move-aside rename, so the context lapses from within that
			// rename: the entry check of Install must not fire first.
			i.renameFn = func(oldname, newname string) error {
				if oldname == target && newname == backup {
					trigger()

					return os.Rename(oldname, newname)
				}

				if oldname == backup && newname == target {
					return errInjectedRestore
				}

				return os.Rename(oldname, newname)
			}

			backupPath, err := i.Install(ctx, stagedFile(t, dir, "v2"))

			trigger()

			if err == nil {
				t.Fatal("Install should fail on the triggered context with a failing restoration")
			}

			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want the original %v in the chain", err, tt.want)
			}

			if !errors.Is(err, ErrReplaced) {
				t.Errorf("err = %v, want ErrReplaced for the displaced target", err)
			}

			if !errors.Is(err, errInjectedRestore) {
				t.Errorf("err = %v, want the restoration failure in the chain", err)
			}

			if backupPath != backup {
				t.Errorf("backup path = %q, want %q so the caller can recover", backupPath, backup)
			}

			if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("target stat = %v, want the displaced (missing) target", statErr)
			}
		})
	}

	t.Run("original restored keeps reporting the cancellation", func(t *testing.T) {
		dir := t.TempDir()

		target := filepath.Join(dir, "app")
		backup := target + BackupSuffix

		writeTarget(t, target, "v1", 0o755)

		i := newWindowsSequenceInstaller(t, target)

		ctx, cancel := context.WithCancel(t.Context())

		i.renameFn = func(oldname, newname string) error {
			if oldname == target && newname == backup {
				cancel()

				return os.Rename(oldname, newname)
			}

			return os.Rename(oldname, newname)
		}

		_, err := i.Install(ctx, stagedFile(t, dir, "v2"))

		cancel()

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}

		if got := readFileForTest(t, target); got != "v1" {
			t.Errorf("target content = %q, want the restored v1", got)
		}

		if _, statErr := os.Lstat(backup); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("backup stat = %v, want the backup consumed by the restore", statErr)
		}
	})
}

// Regression: the pre-publication re-check of the backup path only rejected
// an existing backup; a path that could not be inspected at all fell through
// to the publishing rename, which would have replaced whatever sits there —
// the exact destruction the re-check exists to prevent. An inspection
// failure now disqualifies the publication like an existing backup does.
func TestPublishBackupRejectsUninspectableBackupPath(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	// Force the copy fallback: with the link refused, the backup goes
	// through the staged-copy path with its pre-publication re-check.
	i.linkFn = func(oldname, newname string) error {
		return errors.New("injected: link not supported")
	}

	i.lstatFn = func(name string) (os.FileInfo, error) {
		if name == target+BackupSuffix {
			return nil, errors.New("injected inspection failure")
		}

		return os.Lstat(name)
	}

	_, err := i.Install(t.Context(), stagedFile(t, dir, "v2"))
	if err == nil {
		t.Fatal("Install should fail when the backup path cannot be inspected before publication")
	}

	if errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want an inspection failure, not ErrBackupExists", err)
	}

	if !strings.Contains(err.Error(), "inspecting") {
		t.Fatalf("err = %v, want it to report the inspection failure", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target was modified: %q", got)
	}

	// The staged backup copy must not survive the failed publication.
	// (Only the tmp sweep: the staged input file itself lives in the same
	// directory and legitimately matches the new* prefix.)
	for _, e := range dirEntries(t, dir) {
		if strings.Contains(e, ".go4updates.tmp") {
			t.Errorf("backup staging leftover %q was not removed", e)
		}
	}
}

func TestInstallConcurrentSerialization(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		backupErr bool
	)

	for n := range 2 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			stagedPath := filepath.Join(dir, fmt.Sprintf("staged-%d", n))

			if err := os.WriteFile(stagedPath, []byte("v2"), 0o600); err != nil {
				t.Errorf("writing staged file: %v", err)

				return
			}

			_, err := i.Install(t.Context(), stagedPath)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, ErrBackupExists):
				backupErr = true
			default:
				t.Errorf("unexpected install error: %v", err)
			}
		}()
	}

	wg.Wait()

	if succeeded != 1 || !backupErr {
		t.Errorf("outcomes: succeeded=%d backupErr=%v, want exactly one success and one ErrBackupExists", succeeded, backupErr)
	}
}

func TestRepeatedUpdateWithCleanupBetween(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	i := newTestInstaller(t, target)

	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v2")); err != nil {
		t.Fatalf("install v2: %v", err)
	}

	// v3 without cleanup is refused: the v1 backup is unconfirmed.
	if _, err := i.Install(t.Context(), stagedFile(t, dir, "v3")); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr == nil {
		t.Error("backup should be gone after Cleanup")
	}

	backup, err := i.Install(t.Context(), stagedFile(t, dir, "v3"))
	if err != nil {
		t.Fatalf("install v3: %v", err)
	}

	if backup == "" {
		t.Fatal("v3 install should back up v2")
	}

	if got := readFileForTest(t, backup); got != "v2" {
		t.Errorf("backup content = %q, want v2", got)
	}
}

func TestCleanupRestoresMissingTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	backup := target + BackupSuffix
	writeTarget(t, backup, "v1", 0o755)

	// Simulate the crash window: the target is gone, the backup remains.
	if err := os.Remove(target); err != nil {
		t.Fatalf("removing target: %v", err)
	}

	i := newTestInstaller(t, target)

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if got := readFileForTest(t, target); got != "v1" {
		t.Errorf("target content = %q, want restored v1", got)
	}

	if _, statErr := os.Lstat(backup); statErr == nil {
		t.Error("backup should be gone after the restore")
	}
}

func TestCleanupRecoveryWithExplicitTarget(t *testing.T) {
	// The launched-from-backup scenario: the process runs from the backup
	// path, so os.Executable is wrong; recovery needs WithTargetPath.
	dir := t.TempDir()

	original := filepath.Join(dir, "app")
	backup := original + BackupSuffix

	writeTarget(t, backup, "v1", 0o755)

	i, err := NewInstaller(WithTargetPath(original))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if got := readFileForTest(t, original); got != "v1" {
		t.Errorf("original target content = %q, want restored v1", got)
	}
}

func TestCleanupRemovesStagingArtifacts(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	artifacts := []string{
		target + ".go4updates.download123456",
		target + ".go4updates.new789012",
		target + ".go4updates.tmp345678",
	}

	for _, a := range artifacts {
		writeTarget(t, a, "litter", 0o600)
	}

	// Not an artifact: another binary's files and unrelated names.
	writeTarget(t, filepath.Join(dir, "other.go4updates.download1"), "other", 0o600)
	writeTarget(t, filepath.Join(dir, "app.backup"), "keep", 0o600)

	i := newTestInstaller(t, target)

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	for _, a := range artifacts {
		if _, statErr := os.Lstat(a); statErr == nil {
			t.Errorf("artifact %q survived Cleanup", a)
		}
	}

	if got := readFileForTest(t, filepath.Join(dir, "other.go4updates.download1")); got != "other" {
		t.Error("another binary's artifact must not be removed")
	}

	if got := readFileForTest(t, filepath.Join(dir, "app.backup")); got != "keep" {
		t.Error("unrelated file must not be removed")
	}
}

func TestCleanupKeepsBackupWhenRecoveryFails(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	backup := target + BackupSuffix

	writeTarget(t, backup, "v1", 0o755)

	i := newTestInstaller(t, target)

	// The crash-window recovery fails (for example a locked target name on
	// Windows): the backup is then the only remaining copy of the old
	// binary and must survive Cleanup for the next startup.
	i.renameFn = func(_, _ string) error {
		return errors.New("injected rename failure")
	}

	err := i.Cleanup(t.Context())
	if err == nil {
		t.Fatal("Cleanup should report the failed recovery")
	}

	if !strings.Contains(err.Error(), "restoring") {
		t.Errorf("error should mention the failed restore, got: %v", err)
	}

	if got := readFileForTest(t, backup); got != "v1" {
		t.Errorf("backup content = %q, want v1 (backup must survive a failed recovery)", got)
	}

	if _, statErr := os.Lstat(target); !errors.Is(statErr, fs.ErrNotExist) {
		t.Error("target should still be missing after the failed recovery")
	}
}

func TestCleanupBestEffortWithUnremovableFiles(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)
	writeTarget(t, target+BackupSuffix, "v0", 0o755)
	writeTarget(t, target+".go4updates.download1", "litter", 0o600)

	i := newTestInstaller(t, target)

	// Removal failures are injected through the seam instead of produced
	// with directory permission bits: CI containers run as root, and root
	// ignores write-permission bits, so the permission trick is not
	// deterministic across environments.
	i.removeFn = func(path string) error {
		return fmt.Errorf("injected removal failure for %s", path)
	}

	err := i.Cleanup(t.Context())
	if err == nil {
		t.Fatal("Cleanup should report the unremovable artifacts")
	}

	// Both blocked entries must be reported: reaching the backup removal
	// after the staging failure also proves the sweep is best-effort.
	if !strings.Contains(err.Error(), BackupSuffix) {
		t.Errorf("error should name the blocked backup, got: %v", err)
	}

	if !strings.Contains(err.Error(), ".go4updates.download1") {
		t.Errorf("error should name the blocked staging artifact, got: %v", err)
	}
}

// Regression: Apply and the backup copy fallback create staging artifacts
// with a sanitized base name (os.CreateTemp pattern), but Cleanup matched
// against the raw base name, so a target whose name contains '*' never had
// its staging litter collected.
func TestCleanupMatchesSanitizedStagingNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("* is not legal in Windows file names; the sanitization mismatch is Unix-only")
	}

	dir := t.TempDir()

	target := filepath.Join(dir, "my*app")

	writeTarget(t, target, "v2", 0o755)

	// Artifacts as Apply creates them: sanitized base name.
	writeTarget(t, filepath.Join(dir, "my_app.go4updates.download1"), "litter", 0o600)
	writeTarget(t, filepath.Join(dir, "my_app.go4updates.new2"), "litter", 0o600)

	// Another binary's artifact and an unrelated file must survive.
	writeTarget(t, filepath.Join(dir, "other.go4updates.download1"), "other", 0o600)
	writeTarget(t, filepath.Join(dir, "app.backup"), "keep", 0o600)

	i := newTestInstaller(t, target)

	if err := i.Cleanup(t.Context()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	for _, name := range []string{"my_app.go4updates.download1", "my_app.go4updates.new2"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("staging artifact %q survived Cleanup", name)
		}
	}

	if got := readFileForTest(t, filepath.Join(dir, "other.go4updates.download1")); got != "other" {
		t.Error("another binary's artifact must not be removed")
	}

	if got := readFileForTest(t, filepath.Join(dir, "app.backup")); got != "keep" {
		t.Error("unrelated file must not be removed")
	}

	if got := readFileForTest(t, target); got != "v2" {
		t.Error("the target itself must not be removed")
	}
}

func TestCleanupFreshInstallerIsClean(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "never-installed")

	i := newTestInstaller(t, target)

	if err := i.Cleanup(t.Context()); err != nil {
		t.Errorf("Cleanup on a fresh target: %v", err)
	}
}

// The ctx contract: a canceled context stops the cleanup between its steps.
// Work already completed stays done, and a canceled run never retires the
// backup — the rollback material is not destroyed by the run that was told
// to stop.
func TestCleanupCanceledContextRetainsBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v2", 0o755)

	if err := os.WriteFile(target+BackupSuffix, []byte("v1"), 0o755); err != nil {
		t.Fatalf("writing backup: %v", err)
	}

	// A leftover staging artifact so the sweep has something to remove.
	leftover := filepath.Join(dir, "app.go4updates.tmp123")

	if err := os.WriteFile(leftover, []byte("litter"), 0o600); err != nil {
		t.Fatalf("writing leftover: %v", err)
	}

	i := newTestInstaller(t, target)

	ctx, cancel := context.WithCancel(t.Context())

	// Cancel from the middle of the staging sweep: the removal in progress
	// completes, the checkpoint after the step aborts before the backup
	// retirement.
	first := true

	i.removeFn = func(name string) error {
		if first {
			first = false

			cancel()
		}

		return os.Remove(name)
	}

	err := i.Cleanup(ctx)
	cancel()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if _, statErr := os.Lstat(leftover); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("leftover stat = %v, want the in-progress removal kept", statErr)
	}

	if _, statErr := os.Lstat(target + BackupSuffix); statErr != nil {
		t.Errorf("backup stat = %v, want the backup retained by a canceled cleanup", statErr)
	}
}

func TestTargetPathReportsResolvedTarget(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "app")
	writeTarget(t, target, "v1", 0o755)

	link := filepath.Join(dir, "app-link")

	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	i, err := NewInstaller(WithTargetPath(link))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if i.TargetPath() != target {
		t.Errorf("TargetPath = %q, want resolved %q", i.TargetPath(), target)
	}
}

func TestWithTargetPathValidation(t *testing.T) {
	if _, err := NewInstaller(WithTargetPath("")); err == nil {
		t.Error("empty target path should be rejected")
	}

	if _, err := NewInstaller(WithTargetPath("relative/app")); err == nil {
		t.Error("relative target path should be rejected")
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))

	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}
