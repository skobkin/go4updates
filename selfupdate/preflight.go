package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
)

// CheckInstallability reports — best effort and advisory — whether an
// update could plausibly be applied to this Updater's target, without
// applying anything. It is meant to run before offering an Update action,
// so obvious blockers surface before the user commits to a download.
//
// The check acquires the Updater-wide lifecycle lock (see [Updater]) and
// the Installer mutex, so it never races the staging-artifact sweep of a
// concurrent Apply or Cleanup on the same Updater; a call made while
// another Apply or Cleanup runs waits for it, and a context canceled while
// waiting fails the call. UIs should invoke it off the UI thread.
//
// Checked in order, returning the first finding:
//
//  1. a canceled context;
//  2. an existing unconfirmed backup ([ErrBackupExists]) — confirm the
//     installed version is healthy and call [Updater.Cleanup], then retry;
//  3. the target: a non-regular file ([ErrTargetNotRegular]), or — on
//     Unix, when no mode is forced with [WithFileMode] — the absence of
//     executable permission bits ([ErrTargetNotExecutable]). A missing
//     target passes: installing to a fresh path is supported. Permission
//     problems surface wrapped, so errors.Is finds fs.ErrPermission;
//  4. a staging probe: two uniquely named files are created in the
//     target's directory, the second is removed, the first is renamed onto
//     that verified-free name, and the result is removed again. This
//     proves the create-and-rename capability the replacement sequence
//     needs — including a read-only filesystem ([fs.ErrPermission] or the
//     platform's read-only error stays in the chain).
//
// The probe never renames over an existing file, never touches the target
// executable or the backup, removes every file it created, and reports a
// failing probe removal instead of silently passing with leftovers. Probe
// files use the ".go4updates.tmp" staging name, so files left by a hard
// crash are swept by a later [Updater.Cleanup].
//
// A nil error means no obvious blocker was found — it is not a guarantee
// that Apply will succeed. Preflight cannot predict asset selection,
// download, verification, or extraction failures, and on Windows it cannot
// prove that moving the running executable aside will succeed: directory
// probes do not test the file locks of a running binary. Package-managed
// installs may be writable only under elevated privileges — preflight
// never attempts privilege escalation and is not a substitute for
// application or distribution policy.
func (u *Updater) CheckInstallability(ctx context.Context) error {
	releaseLifecycle, err := u.acquireLifecycle(ctx)
	if err != nil {
		return err
	}

	defer releaseLifecycle()

	return u.installer.checkInstallability(ctx)
}

// checkInstallability implements the CheckInstallability checks on the
// Installer's target. The Installer mutex serializes it against Install
// and Cleanup, keeping the probe safe from the staging sweep.
func (i *Installer) checkInstallability(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("go4updates/selfupdate: installability check canceled: %w", err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	target := i.targetPath

	// An unconfirmed backup blocks the replacement before anything else
	// matters — including before the target check, so the Windows
	// crash-recovery shape (target missing, backup present) is reported as
	// the backup it is, not as a missing-target pass.
	if err := i.checkBackupAbsent(target + BackupSuffix); err != nil {
		return err
	}

	info, err := i.lstatFn(target)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A fresh target is installable when the directory allows staging;
		// the install handles the fresh path without a backup.

	case err != nil:
		return fmt.Errorf("go4updates/selfupdate: inspecting %s: %w", target, err)

	case !info.Mode().IsRegular():
		return fmt.Errorf("%w: %s", ErrTargetNotRegular, target)

	case runtime.GOOS != "windows" && !i.fileModeSet && info.Mode().Perm()&0o111 == 0:
		return fmt.Errorf("%w: %s has mode %v, which has no execute bit: the installed binary could not be run; set executable permission bits with WithFileMode or chmod the target",
			ErrTargetNotExecutable, target, info.Mode())
	}

	return i.probeStaging(filepath.Dir(target), filepath.Base(target))
}

// probeStaging proves that files can be created and renamed inside dir —
// the capabilities the replacement sequence needs — without touching the
// target executable or the backup. The rename destination is verified
// free instead of relying on rename-over-existing semantics, which differ
// across platforms and can produce misleading results on Windows.
//
// Every path the probe creates is removed on success and on every failure
// path; a failing removal is reported next to the original error, and an
// otherwise-successful probe with a failing cleanup fails the check
// instead of passing with leftovers. An already-removed path is treated
// as cleaned up.
func (i *Installer) probeStaging(dir, targetBase string) error {
	pattern := sanitizePattern(targetBase) + ".go4updates.tmp*"

	src, err := i.createTempFn(dir, pattern)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: creating a staging probe file in %s: %w", dir, err)
	}

	srcName := src.Name()

	if err := src.Close(); err != nil {
		return i.failProbe(err, srcName)
	}

	dst, err := i.createTempFn(dir, pattern)
	if err != nil {
		return i.failProbe(
			fmt.Errorf("go4updates/selfupdate: creating the probe rename destination in %s: %w", dir, err),
			srcName)
	}

	dstName := dst.Name()

	if err := dst.Close(); err != nil {
		return i.failProbe(err, srcName, dstName)
	}

	// The destination must verifiably not exist when the rename lands:
	// rename-over-existing behavior differs across platforms.
	if err := i.removeFn(dstName); err != nil {
		return i.failProbe(
			fmt.Errorf("go4updates/selfupdate: clearing the probe rename destination in %s: %w", dir, err),
			srcName, dstName)
	}

	if err := i.renameFn(srcName, dstName); err != nil {
		return i.failProbe(
			fmt.Errorf("go4updates/selfupdate: renaming within %s: %w", dir, err),
			srcName)
	}

	// The rename consumed srcName; only dstName remains.
	if err := i.removeFn(dstName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("go4updates/selfupdate: removing the staging probe file %s: %w", dstName, err)
	}

	return nil
}

// failProbe removes every owned probe path after a failure and reports
// the result: the original error when the cleanup succeeds, or the
// original error joined with the removal failures when it does not — so
// the first diagnosis stays first and no leftover goes unreported.
func (i *Installer) failProbe(err error, owned ...string) error {
	var removalErrs []error

	for _, name := range owned {
		if rmErr := i.removeFn(name); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			removalErrs = append(removalErrs, rmErr)
		}
	}

	if len(removalErrs) == 0 {
		return err
	}

	return errors.Join(append([]error{err}, removalErrs...)...)
}
