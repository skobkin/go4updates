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
)

// BackupSuffix is appended to the target binary name to form the name of
// the previous version retained after a successful update. The backup is
// removed only by [Installer.Cleanup], which applications should call after
// confirming the installed version is healthy. Cleanup is an artifact
// collector, not a rollback mechanism.
const BackupSuffix = ".go4updates.old"

// Installer atomically replaces a target binary with a staged replacement
// file living on the same filesystem, preserving the previous version under
// a [BackupSuffix]-named backup until the application confirms it can be
// removed.
//
// Replacement guarantees differ by platform:
//
// On Unix the previous binary is preserved under the backup name before the
// staged file takes the target name with a single rename, so the target path
// always refers to a complete executable — never missing, never torn. This
// is an atomic pathname replacement; durability after sudden power loss
// additionally depends on platform fsync behavior (the staged file is always
// synced; the containing directory is synced, which is strongest on Linux).
// Platforms or filesystems that cannot sync a directory at all are
// tolerated as the documented best effort — a failed sync that cannot
// physically succeed does not report a completed replacement as a failure —
// while genuine sync I/O errors surface (wrapped in [ErrReplaced] when they
// arrive after the replacement).
//
// On Windows a running executable cannot be deleted or written, only moved
// aside, so the replacement moves the old binary to the backup name first
// and renames the staged file into place second. A crash between the two
// renames leaves the target path missing and automatic recovery impossible:
// recover manually by renaming "<exe>.go4updates.old" back to "<exe>". The
// sequence is not atomic and this is a documented limitation.
//
// Installs through one Installer are serialized. Cross-process updates of
// one target are unsupported: the backup-existence checks fail fast in the
// common races, and backup publication is no-clobber where the filesystem
// provides atomic hardlinks, but this is not a locking system.
type Installer struct {
	targetPath  string
	fileMode    fs.FileMode
	fileModeSet bool

	// windowsSequence selects the Windows two-rename replacement in
	// Install. Production wires it from runtime.GOOS; it is a field so the
	// displaced-target restore paths stay regression-testable on every
	// platform instead of only on native Windows runners.
	windowsSequence bool

	mu sync.Mutex

	// The following fields are seams for fault-injection tests; production
	// constructors wire them to the os-level implementations.
	linkFn    func(oldname, newname string) error
	copyFn    func(dst io.Writer, srcPath string) error
	renameFn  func(oldname, newname string) error
	syncDirFn func(dir string) error
	removeFn  func(name string) error
	lstatFn   func(name string) (os.FileInfo, error)
}

// NewInstaller creates an Installer from opts. It understands
// [WithTargetPath] (default: the running executable with symbolic links
// resolved) and [WithFileMode].
func NewInstaller(opts ...Option) (*Installer, error) {
	o, err := applyOptions("go4updates/selfupdate", opts)
	if err != nil {
		return nil, err
	}

	target := o.targetPath

	if target == "" {
		target, err = runningExecutablePath()
		if err != nil {
			return nil, err
		}
	} else {
		target = resolveSymlinks(target)
	}

	return &Installer{
		targetPath:      target,
		fileMode:        o.fileMode,
		fileModeSet:     o.fileModeSet,
		windowsSequence: runtime.GOOS == "windows",

		linkFn:    os.Link,
		copyFn:    copyFileTo,
		renameFn:  os.Rename,
		syncDirFn: syncDir,
		removeFn:  os.Remove,
		lstatFn:   os.Lstat,
	}, nil
}

// TargetPath reports the resolved installation target path.
func (i *Installer) TargetPath() string {
	return i.targetPath
}

// runningExecutablePath resolves the path of the running executable with
// symbolic links resolved.
func runningExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("go4updates/selfupdate: resolving the running executable: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("go4updates/selfupdate: resolving %s: %w", exe, err)
	}

	return resolved, nil
}

// resolveSymlinks resolves symbolic links in an existing target path; for a
// not-yet-existing target, the existing parent directory is resolved.
func resolveSymlinks(target string) string {
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return resolved
	}

	dir := filepath.Dir(target)

	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(target))
	}

	return target
}

// Install atomically replaces the target binary with stagedPath and returns
// the backup path of the previous version (empty when the target did not
// previously exist).
//
// A backup from an earlier unconfirmed install makes Install fail with
// [ErrBackupExists] — whether or not the target itself exists: after a
// Windows crash-window failure the target is missing while the backup
// remains, and installing over that state would strand the backup.
//
// Failure semantics: when Install returns an error, the target was left
// untouched unless errors.Is(err, [ErrReplaced]) — that sentinel reports
// that the attempt changed the target name before the failure: either the
// replacement already took effect (the error concerns post-replacement
// work only), or — the Windows restore-failure paths — the target was
// moved aside and could not be restored, so the target path is missing
// and the returned backup path holds the old executable; in both shapes
// [Installer.Cleanup] recovers a consistent state before a retry. On the
// restore-failure paths the original installation failure (for example
// the context cancellation) and the restoration failure are joined, not
// replaced, so errors.Is identifies each of the three. A
// backup created by this failed attempt is removed again so a retry is
// not blocked by [ErrBackupExists]; a pre-existing backup is never
// created, written, or removed by Install.
func (i *Installer) Install(ctx context.Context, stagedPath string) (backupPath string, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w", err)
	}

	target := i.targetPath
	backup := target + BackupSuffix

	if i.windowsSequence {
		return i.installWindows(ctx, stagedPath, target, backup)
	}

	return i.installUnix(ctx, stagedPath, target, backup)
}

// checkBackupAbsent rejects the install while a backup from an earlier
// unconfirmed install exists.
func checkBackupAbsent(backup string) error {
	_, statErr := os.Lstat(backup)

	switch {
	case statErr == nil:
		return fmt.Errorf("%w: %s (confirm the installed version is healthy, call Cleanup, then retry)", ErrBackupExists, backup)

	case errors.Is(statErr, fs.ErrNotExist):
		return nil

	default:
		return fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", backup, statErr)
	}
}

// installUnix is the Unix replacement sequence described on Installer.
func (i *Installer) installUnix(ctx context.Context, stagedPath, target, backup string) (string, error) {
	if err := checkBackupAbsent(backup); err != nil {
		return "", err
	}

	if err := i.prepareStaged(stagedPath, target); err != nil {
		return "", err
	}

	_, statErr := os.Lstat(target)

	switch {
	case statErr == nil:
		// Preserve the old binary before the new one takes the name.

	case errors.Is(statErr, fs.ErrNotExist):
		// Nothing to preserve: install to the fresh path.
		if err := i.renameFn(stagedPath, target); err != nil {
			return "", fmt.Errorf("go4updates/selfupdate: installing %s: %w", target, err)
		}

		if err := i.syncDirFn(filepath.Dir(target)); err != nil && !dirSyncUnsupported(err) {
			return "", fmt.Errorf("%w: syncing %s after the installation: %w",
				ErrReplaced, filepath.Dir(target), err)
		}

		return "", nil

	default:
		return "", fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", target, statErr)
	}

	backupCreated, err := i.publishBackup(target, backup)
	if err != nil {
		return "", err
	}

	// Best-effort durability of the backup publication.
	_ = i.syncDirFn(filepath.Dir(target))

	if err := ctx.Err(); err != nil {
		if backupCreated {
			// The attempt backup is redundant now; its removal failure is
			// folded into the error instead of being dropped, so a retry
			// blocked by ErrBackupExists can be explained.
			if rmErr := i.removeFn(backup); rmErr != nil {
				return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w (removing the attempt backup %s also failed: %v)",
					err, backup, rmErr)
			}
		}

		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w", err)
	}

	if err := i.renameFn(stagedPath, target); err != nil {
		// The target was never touched. The backup created by this attempt
		// is now redundant and is removed so a retry is not blocked; a
		// pre-existing backup never reaches this path (checked above).
		if backupCreated {
			if rmErr := i.removeFn(backup); rmErr != nil {
				return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w (removing the attempt backup %s also failed: %v)",
					target, err, backup, rmErr)
			}
		}

		return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w", target, err)
	}

	if err := i.syncDirFn(filepath.Dir(target)); err != nil && !dirSyncUnsupported(err) {
		// The replacement is in effect; the error must not suggest
		// otherwise.
		return backup, fmt.Errorf("%w: syncing %s after the replacement: %w",
			ErrReplaced, filepath.Dir(target), err)
	}

	return backup, nil
}

// installWindows is the Windows replacement sequence described on
// Installer: a running executable can only be moved aside, not replaced in
// place, which leaves a documented two-rename crash window.
func (i *Installer) installWindows(ctx context.Context, stagedPath, target, backup string) (string, error) {
	// The backup guard applies here too: os.Rename on Windows replaces an
	// existing destination, so without the guard the move-aside step would
	// silently overwrite an unconfirmed backup and destroy the rollback
	// material.
	if err := checkBackupAbsent(backup); err != nil {
		return "", err
	}

	if err := i.prepareStaged(stagedPath, target); err != nil {
		return "", err
	}

	_, statErr := os.Lstat(target)

	switch {
	case statErr == nil:
		// Move the running executable aside; deletion is never attempted.
		if err := i.renameFn(target, backup); err != nil {
			return "", fmt.Errorf("go4updates/selfupdate: moving %s aside: %w", target, err)
		}

	case errors.Is(statErr, fs.ErrNotExist):
		// Fresh target: single rename.
		if err := i.renameFn(stagedPath, target); err != nil {
			return "", fmt.Errorf("go4updates/selfupdate: installing %s: %w", target, err)
		}

		return "", nil

	default:
		return "", fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", target, statErr)
	}

	if err := ctx.Err(); err != nil {
		// Restore the old binary: the target path must not stay missing.
		if rerr := i.renameFn(backup, target); rerr != nil {
			// The target is missing and could not be restored, so the
			// "target untouched" contract cannot be honored: report the
			// displaced state through [ErrReplaced] and return the backup
			// path so the caller can recover. The context error and the
			// restoration failure are joined, so errors.Is identifies
			// both — the original failure is not lost behind the restore.
			return backup, fmt.Errorf("%w: install canceled: %w; recover manually: move %s %s",
				ErrReplaced,
				errors.Join(err, fmt.Errorf("restoring %s: %w", backup, rerr)),
				backup, target)
		}

		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w (original restored)", err)
	}

	if err := i.renameFn(stagedPath, target); err != nil {
		if rerr := i.renameFn(backup, target); rerr != nil {
			// Displaced target, same as above: the old binary lives only
			// under the backup name. The replacement failure and the
			// restoration failure are joined, so errors.Is identifies
			// both.
			return backup, fmt.Errorf("%w: replacing %s: %w; recover manually: move %s %s",
				ErrReplaced, target,
				errors.Join(err, fmt.Errorf("restoring %s: %w", backup, rerr)),
				backup, target)
		}

		return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w (original restored)", target, err)
	}

	if err := i.syncDirFn(filepath.Dir(target)); err != nil && !dirSyncUnsupported(err) {
		return backup, fmt.Errorf("%w: syncing %s after the replacement: %w",
			ErrReplaced, filepath.Dir(target), err)
	}

	return backup, nil
}

// prepareStaged applies the final permission bits to the staged file and
// syncs it, so the bytes published under the target name are complete and
// durable before the rename.
//
// The final mode must carry an execute bit on Unix — the installed file has
// to be runnable. It comes from [WithFileMode] or, when unset, from the
// mode of the binary being replaced; an inherited mode without an execute
// bit fails the install with the target untouched, naming the remedy.
//
// The writable handle is opened before the mode changes: opening a file for
// writing after chmod 0555 fails with EACCES for unprivileged users, while
// a handle already open keeps its access (permission is checked at open
// time), so the sync below still works for read-only executable modes.
func (i *Installer) prepareStaged(stagedPath, target string) error {
	mode := fs.FileMode(0o755)

	switch {
	case i.fileModeSet:
		mode = i.fileMode

	default:
		if info, err := os.Stat(target); err == nil {
			mode = info.Mode().Perm()
		}
	}

	if runtime.GOOS != "windows" && mode.Perm()&0o111 == 0 {
		return fmt.Errorf("go4updates/selfupdate: %s has mode %v, which has no execute bit: the installed binary could not be run; set executable permission bits with WithFileMode or chmod the target", target, mode)
	}

	// The handle must be writable: on Windows, FlushFileBuffers (what
	// File.Sync calls) requires GENERIC_WRITE access, so syncing a file
	// opened read-only fails with "Access is denied" and would make every
	// install fail there before any rename.
	f, err := os.OpenFile(stagedPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening %s: %w", stagedPath, err)
	}

	defer func() { _ = f.Close() }()

	if err := os.Chmod(stagedPath, mode); err != nil {
		return fmt.Errorf("go4updates/selfupdate: setting mode on %s: %w", stagedPath, err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("go4updates/selfupdate: syncing %s: %w", stagedPath, err)
	}

	return nil
}

// publishBackup preserves the current target under backup: a hardlink where
// the filesystem supports it, otherwise a staged copy published without
// clobbering — the staging file is linked onto the backup name, which fails
// with EEXIST instead of replacing whatever appeared there, and only a
// filesystem without hardlink support falls back to a single rename. The
// backup path was verified free just before publication; EEXIST at link
// time is the concurrent-install race and surfaces as [ErrBackupExists] —
// it never triggers a fallback, and an existing backup is never overwritten
// or truncated while this process installs. The rename fallback retains the
// documented cross-process window: cross-process updates of one target are
// unsupported.
func (i *Installer) publishBackup(target, backup string) (created bool, err error) {
	linkErr := i.linkFn(target, backup)
	if linkErr == nil {
		return true, nil
	}

	if errors.Is(linkErr, fs.ErrExist) {
		return false, fmt.Errorf("%w: %s appeared concurrently (cross-process updates are unsupported)",
			ErrBackupExists, backup)
	}

	// Hardlinks are unsupported on this filesystem (or otherwise refused):
	// stage a copy under a temporary name, sync it, and publish it through
	// a no-clobber step so a partial copy is never visible under the backup
	// name and a racing backup is never replaced.
	dir := filepath.Dir(target)
	base := filepath.Base(target)

	tmp, err := os.CreateTemp(dir, sanitizePattern(base)+".go4updates.tmp*")
	if err != nil {
		return false, fmt.Errorf("go4updates/selfupdate: creating backup staging file: %w", err)
	}

	if err := i.copyFn(tmp, target); err != nil {
		_ = tmp.Close()

		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: copying %s to the backup: %w", target, err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: syncing the backup copy: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: closing the backup copy: %w", err)
	}

	// Re-check before publication so a racing backup fails fast. A path
	// that cannot be inspected disqualifies the publication just like an
	// existing one.
	switch _, err := i.lstatFn(backup); {
	case err == nil:
		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("%w: %s appeared concurrently (cross-process updates are unsupported)",
			ErrBackupExists, backup)

	case errors.Is(err, fs.ErrNotExist):
		// The path is free; publish.

	default:
		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", backup, err)
	}

	// Publishing through a link is atomic and no-clobber: if another
	// process created the backup between the check above and now, the link
	// fails with EEXIST instead of replacing it. tmp sits in the same
	// directory as the backup, so the link cannot cross a filesystem
	// boundary.
	publishErr := i.linkFn(tmp.Name(), backup)

	switch {
	case publishErr == nil:
		_ = i.removeFn(tmp.Name())

		return true, nil

	case errors.Is(publishErr, fs.ErrExist):
		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("%w: %s appeared concurrently (cross-process updates are unsupported)",
			ErrBackupExists, backup)
	}

	// No hardlink support here either (the condition that forced the copy):
	// publish with a rename. os.Rename replaces an existing destination, so
	// the re-check above is the only guard and the residual cross-process
	// window is a documented limitation.
	if err := i.renameFn(tmp.Name(), backup); err != nil {
		_ = i.removeFn(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: publishing backup %s: %w", backup, err)
	}

	return true, nil
}

// Cleanup removes selfupdate artifacts around the target and recovers an
// interrupted install where possible. It is best-effort: per-file failures
// are collected and reported together, never fatal to the other steps.
//
// The steps, in order:
//
//  1. Recovery: when the target is missing but the backup exists (the
//     Windows crash window), the backup is renamed back onto the target.
//     This requires knowing the original target path — when the application
//     is launched from the backup executable itself, os.Executable resolves
//     to the backup path and automatic recovery needs an explicit
//     [WithTargetPath]; otherwise recovery is a manual rename.
//  2. Removal of leftover download, staging, and backup-copy files from
//     interrupted installs.
//  3. Removal of the backup of the previous version. The backup is retired
//     only when the target was confirmed present or just restored: when
//     recovery failed, the backup is the only remaining copy of the
//     executable, so it is kept for the next startup instead of being
//     destroyed by the very run that noticed the problem.
//
// The library never calls Cleanup on its own. Applications should call it
// after successful initialization or a health check — reaching the new
// binary proves it starts, not that it is healthy — and never earlier,
// because the backup is the only rollback material until then. On Windows,
// removing a backup whose old process still runs fails with a sharing
// violation; the error is collected and the removal retried successfully at
// the next startup.
//
// ctx is checked between the steps: a canceled context stops the cleanup
// and returns the cancellation error joined with the problems collected so
// far, keeping everything not yet processed — a canceled run never retires
// the backup.
func (i *Installer) Cleanup(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	target := i.targetPath
	dir := filepath.Dir(target)

	// Staging artifacts are created with the sanitized base name (it feeds
	// an os.CreateTemp pattern), so the sweep must match the same form.
	base := sanitizePattern(filepath.Base(target))

	backup := target + BackupSuffix

	var problems []error

	// canceled aborts the remaining steps when ctx is done, reporting the
	// cancellation together with the problems collected so far.
	canceled := func() error {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(problems,
				fmt.Errorf("go4updates/selfupdate: cleanup canceled: %w", err))...)
		}

		return nil
	}

	if err := canceled(); err != nil {
		return err
	}

	// 1. Recovery: restore the backup when the target is missing.
	_, statErr := os.Lstat(target)

	restored := false

	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		if _, berr := os.Lstat(backup); berr == nil {
			if rerr := i.renameFn(backup, target); rerr != nil {
				problems = append(problems, fmt.Errorf("restoring %s: %w", backup, rerr))
			} else {
				restored = true

				_ = i.syncDirFn(dir)
			}
		} else if !errors.Is(berr, fs.ErrNotExist) {
			problems = append(problems, fmt.Errorf("inspecting %s: %w", backup, berr))
		}

	case statErr != nil:
		problems = append(problems, fmt.Errorf("inspecting %s: %w", target, statErr))
	}

	if err := canceled(); err != nil {
		return err
	}

	// 2. Remove leftover staging artifacts.
	entries, err := os.ReadDir(dir)
	if err != nil {
		problems = append(problems, err)
	} else {
		for _, e := range entries {
			if !isStagingArtifact(e.Name(), base) {
				continue
			}

			if err := i.removeFn(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, fmt.Errorf("removing %s: %w", e.Name(), err))
			}
		}
	}

	if err := canceled(); err != nil {
		return err
	}

	// 3. Retire the backup (gone already if it was just restored) — but only
	// when the target is confirmed present or was just restored. After a
	// failed recovery the backup is the only remaining copy of the
	// executable and must survive for the next startup.
	if statErr == nil || restored {
		if err := i.removeFn(backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, fmt.Errorf("removing backup %s: %w", backup, err))
		}
	}

	return errors.Join(problems...)
}

// isStagingArtifact reports whether name is a leftover download, staging,
// or backup-copy file of the target binary.
func isStagingArtifact(name, base string) bool {
	if !strings.HasPrefix(name, base+".go4updates.") {
		return false
	}

	suffix := strings.TrimPrefix(name, base+".go4updates.")

	return strings.HasPrefix(suffix, "download") ||
		strings.HasPrefix(suffix, "new") ||
		strings.HasPrefix(suffix, "tmp")
}

// dirSyncUnsupported reports whether err means the platform or filesystem
// cannot sync a directory at all — EINVAL, ENOTSUP/EOPNOTSUPP, and
// equivalents — as opposed to a genuine I/O failure such as EIO. An
// unsupported sync is tolerated as the documented best effort, so a
// completed replacement is not reported as a failure on filesystems that
// simply do not provide the operation; real disk errors are never silently
// swallowed.
func dirSyncUnsupported(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP)
}

// syncDir syncs a directory entry list to disk. Not portable to Windows,
// where it is a no-op; on some platforms it is weaker than on Linux, which
// the documentation says plainly instead of claiming power-loss
// durability everywhere.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	defer func() { _ = d.Close() }()

	return d.Sync()
}

// copyFileTo copies srcPath's contents and permission bits into dst. Used
// for the backup copy fallback: the backup must be a runnable binary, so
// the mode of the source matters when a restore renames it back.
func copyFileTo(dst io.Writer, srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}

	defer func() { _ = src.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}

	info, err := src.Stat()
	if err != nil {
		return err
	}

	if chowner, ok := dst.(interface{ Chmod(fs.FileMode) error }); ok {
		return chowner.Chmod(info.Mode().Perm())
	}

	return nil
}

// sanitizePattern strips characters with special meaning in os.CreateTemp
// patterns from a file base name.
func sanitizePattern(base string) string {
	return strings.ReplaceAll(base, "*", "_")
}
