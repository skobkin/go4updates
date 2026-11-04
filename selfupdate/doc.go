// Package selfupdate applies release results produced by the updates
// package: it selects the release asset matching the current platform,
// downloads it over HTTPS with hard size bounds, verifies its SHA-256
// checksum, extracts the executable from the archive, and atomically
// replaces the running binary while preserving the previous version for
// recovery.
//
// # Composition
//
// An [Updater] consumes an already-checked [updates.Result]: the decision
// that an update exists belongs to the checker, the watcher, and the
// application. The pipeline steps are also available as composable pieces:
// a [Downloader] streams any release asset to an arbitrary [io.Writer], a
// [Verifier] validates downloaded bytes, and an [Installer] atomically
// replaces a binary with a staged file.
//
// The package is opt-in by import. Applications decide when and whether to
// call [Updater.Apply]; the library never applies updates on its own and
// never re-fetches release metadata.
//
// # Verification
//
// SHA-256 verification against a checksum asset published alongside the
// binary is mandatory. This provides integrity — protection against
// corrupted, truncated, or substituted downloads — but not independent
// publisher authentication: an asset and its checksum come from the same
// release, so a compromised release can publish a matching pair. Transport
// is protected by HTTPS only. Cryptographic publisher signatures are the
// reserved [Verifier] extension point. [WithInsecureSkipVerify] disables
// verification and should only be used when the transport itself is fully
// trusted.
//
// # Progress reporting
//
// [Updater.ApplyWithProgress] reports the pipeline as typed [Progress]
// events: one transition per stage ([StageDownloading] through
// [StageCompleted]), throttled intermediate byte events, one exact final
// byte event per successfully completed stage, and a single completion
// event. A Total of zero means the total is unknown — consumers must not
// derive percentages or ETAs from it; a stage is determinate once any of
// its events carries a nonzero Total. Download totals come from the actual
// response; verification byte counts measure what the verifier actually
// consumed (determinate for the built-in checksum verifier, honestly
// indeterminate for custom [Verifier] implementations).
//
// The callback runs synchronously on the calling goroutine while the
// Updater lifecycle lock is held: it must not call Apply, ApplyWithProgress,
// or Cleanup on the same Updater, and it must not block. The
// [StageInstalling] transition is the boundary after which a UI must stop
// offering cancellation; the outcome of a canceled install is read from
// the returned error (see [ErrReplaced]), never from progress events.
//
// # Installation preflight
//
// [Updater.CheckInstallability] reports, best effort and advisory, whether
// an update could plausibly start: an existing unconfirmed backup, an
// unusable target ([ErrTargetNotRegular], [ErrTargetNotExecutable]), and a
// staging-and-rename probe of the target directory. It never modifies the
// target or the backup, removes every probe file, and takes the Updater
// lifecycle lock, so it waits for a concurrent Apply or Cleanup rather
// than racing one — call it off the UI thread. A nil error is not a
// guarantee that Apply will succeed; on Windows in particular it cannot
// prove that moving the running executable aside will succeed.
//
// # Replacement guarantees
//
// On Unix the target path is replaced atomically: the previous executable is
// preserved under [BackupSuffix] before the new one takes the name, and the
// target name always refers to a complete binary. Durability against sudden
// power loss additionally depends on platform fsync behavior: the staged
// file is always synced, and the containing directory is synced best-effort,
// which is strongest on Linux. Filesystems that cannot sync a directory are
// tolerated (the replacement still reports success); genuine sync I/O
// failures are reported, wrapped in [ErrReplaced] when they arrive after the
// replacement.
//
// On Windows a running executable cannot be replaced in place; the
// replacement moves it aside first. A crash between the two renames leaves
// the target path missing. When the original target path is known,
// [Updater.Cleanup] restores the backup automatically; recovery is a manual
// rename of "<exe>.go4updates.old" back to "<exe>" only when the
// application is launched from the backup itself without an explicit
// [WithTargetPath]. See the repository README for the full support matrix
// and recovery procedures.
//
// The backup is removed only by [Updater.Cleanup] (or [Installer.Cleanup]
// directly), which applications should call after successful initialization
// or a health check — reaching the new binary proves it starts, not that it
// is healthy. Cleanup is an artifact collector, not a rollback mechanism.
package selfupdate
