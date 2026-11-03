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
//
// On Windows a running executable cannot be deleted or written, only moved
// aside, so the replacement moves the old binary to the backup name first
// and renames the staged file into place second. A crash between the two
// renames leaves the target path missing and automatic recovery impossible:
// recover manually by renaming "<exe>.go4updates.old" back to "<exe>". The
// sequence is not atomic and this is a documented limitation.
//
// Installs through one Installer are serialized. Cross-process updates of
// one target are unsupported; the backup-existence checks fail fast in the
// common races.
type Installer struct {
	targetPath  string
	fileMode    fs.FileMode
	fileModeSet bool

	mu sync.Mutex

	// The following fields are seams for fault-injection tests; production
	// constructors wire them to the os-level implementations.
	linkFn    func(oldname, newname string) error
	copyFn    func(dst io.Writer, srcPath string) error
	renameFn  func(oldname, newname string) error
	syncDirFn func(dir string) error
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
		targetPath:  target,
		fileMode:    o.fileMode,
		fileModeSet: o.fileModeSet,

		linkFn:    os.Link,
		copyFn:    copyFileTo,
		renameFn:  os.Rename,
		syncDirFn: syncDir,
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
// Failure semantics: when Install returns an error, the target was left
// untouched unless errors.Is(err, [ErrReplaced]) — that sentinel reports a
// replacement that already took effect, with the error concerning
// post-replacement work only. A backup created by this failed attempt is
// removed again so a retry is not blocked by [ErrBackupExists]; a
// pre-existing backup is never created, written, or removed by Install.
func (i *Installer) Install(ctx context.Context, stagedPath string) (backupPath string, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w", err)
	}

	target := i.targetPath
	backup := target + BackupSuffix

	if runtime.GOOS == "windows" {
		return i.installWindows(ctx, stagedPath, target, backup)
	}

	return i.installUnix(ctx, stagedPath, target, backup)
}

// installUnix is the Unix replacement sequence described on Installer.
func (i *Installer) installUnix(ctx context.Context, stagedPath, target, backup string) (string, error) {
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

		if err := i.syncDirFn(filepath.Dir(target)); err != nil {
			return "", fmt.Errorf("%w: syncing %s after the installation: %w",
				ErrReplaced, filepath.Dir(target), err)
		}

		return "", nil

	default:
		return "", fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", target, statErr)
	}

	if _, statErr := os.Lstat(backup); statErr == nil {
		return "", fmt.Errorf("%w: %s (confirm the installed version is healthy, call Cleanup, then retry)",
			ErrBackupExists, backup)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", backup, statErr)
	}

	backupCreated, err := i.publishBackup(target, backup)
	if err != nil {
		return "", err
	}

	// Best-effort durability of the backup publication.
	_ = i.syncDirFn(filepath.Dir(target))

	if err := ctx.Err(); err != nil {
		if backupCreated {
			_ = os.Remove(backup)
		}

		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w", err)
	}

	if err := i.renameFn(stagedPath, target); err != nil {
		// The target was never touched. The backup created by this attempt
		// is now redundant and is removed so a retry is not blocked; a
		// pre-existing backup never reaches this path (checked above).
		if backupCreated {
			if rmErr := os.Remove(backup); rmErr != nil {
				return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w (removing the attempt backup %s also failed: %v)",
					target, err, backup, rmErr)
			}
		}

		return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w", target, err)
	}

	if err := i.syncDirFn(filepath.Dir(target)); err != nil {
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
			return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w; restoring %s also failed: %w; recover manually: move %s %s",
				err, backup, rerr, backup, target)
		}

		return "", fmt.Errorf("go4updates/selfupdate: install canceled: %w (original restored)", err)
	}

	if err := i.renameFn(stagedPath, target); err != nil {
		if rerr := i.renameFn(backup, target); rerr != nil {
			return "", fmt.Errorf("go4updates/selfupdate: replacing %s failed: %w; restoring %s also failed: %w; recover manually: move %s %s",
				target, err, backup, rerr, backup, target)
		}

		return "", fmt.Errorf("go4updates/selfupdate: replacing %s: %w (original restored)", target, err)
	}

	if err := i.syncDirFn(filepath.Dir(target)); err != nil {
		return backup, fmt.Errorf("%w: syncing %s after the replacement: %w",
			ErrReplaced, filepath.Dir(target), err)
	}

	return backup, nil
}

// prepareStaged applies the final permission bits to the staged file and
// syncs it, so the bytes published under the target name are complete and
// durable before the rename.
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

	if err := os.Chmod(stagedPath, mode); err != nil {
		return fmt.Errorf("go4updates/selfupdate: setting mode on %s: %w", stagedPath, err)
	}

	f, err := os.Open(stagedPath)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening %s: %w", stagedPath, err)
	}

	defer func() { _ = f.Close() }()

	if err := f.Sync(); err != nil {
		return fmt.Errorf("go4updates/selfupdate: syncing %s: %w", stagedPath, err)
	}

	return nil
}

// publishBackup preserves the current target under backup: a hardlink where
// the filesystem supports it, otherwise a staged copy published atomically.
// The backup path was verified free just before the call; EEXIST at link
// time is the concurrent-install race and surfaces as [ErrBackupExists] —
// it never triggers the copy fallback, and an existing backup is never
// overwritten or truncated.
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
	// stage a copy under a temporary name, sync it, and publish it with a
	// single rename so a partial copy is never visible under the backup
	// name.
	dir := filepath.Dir(target)
	base := filepath.Base(target)

	tmp, err := os.CreateTemp(dir, sanitizePattern(base)+".go4updates.tmp*")
	if err != nil {
		return false, fmt.Errorf("go4updates/selfupdate: creating backup staging file: %w", err)
	}

	if err := i.copyFn(tmp, target); err != nil {
		_ = tmp.Close()

		_ = os.Remove(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: copying %s to the backup: %w", target, err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		_ = os.Remove(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: syncing the backup copy: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return false, fmt.Errorf("go4updates/selfupdate: closing the backup copy: %w", err)
	}

	// Re-check before publication so a racing backup is never replaced.
	if _, err := os.Lstat(backup); err == nil {
		_ = os.Remove(tmp.Name())

		return false, fmt.Errorf("%w: %s appeared concurrently (cross-process updates are unsupported)",
			ErrBackupExists, backup)
	}

	if err := i.renameFn(tmp.Name(), backup); err != nil {
		_ = os.Remove(tmp.Name())

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
//  3. Removal of the backup of the previous version.
//
// The library never calls Cleanup on its own. Applications should call it
// after successful initialization or a health check — reaching the new
// binary proves it starts, not that it is healthy — and never earlier,
// because the backup is the only rollback material until then. On Windows,
// removing a backup whose old process still runs fails with a sharing
// violation; the error is collected and the removal retried successfully at
// the next startup.
func (i *Installer) Cleanup() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	target := i.targetPath
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	backup := target + BackupSuffix

	var problems []error

	// 1. Recovery: restore the backup when the target is missing.
	_, statErr := os.Lstat(target)

	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		if _, berr := os.Lstat(backup); berr == nil {
			if rerr := i.renameFn(backup, target); rerr != nil {
				problems = append(problems, fmt.Errorf("restoring %s: %w", backup, rerr))
			} else {
				_ = i.syncDirFn(dir)
			}
		} else if !errors.Is(berr, fs.ErrNotExist) {
			problems = append(problems, fmt.Errorf("inspecting %s: %w", backup, berr))
		}

	case statErr != nil:
		problems = append(problems, fmt.Errorf("inspecting %s: %w", target, statErr))
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

			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, fmt.Errorf("removing %s: %w", e.Name(), err))
			}
		}
	}

	// 3. Retire the backup (gone already if it was just restored).
	if err := os.Remove(backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		problems = append(problems, fmt.Errorf("removing backup %s: %w", backup, err))
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
