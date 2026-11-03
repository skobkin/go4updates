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
// # Replacement guarantees
//
// On Unix the target path is replaced atomically: the previous executable is
// preserved under [BackupSuffix] before the new one takes the name, and the
// target name always refers to a complete binary. Durability against sudden
// power loss additionally depends on platform fsync behavior: the staged
// file is always synced, and the containing directory is synced best-effort,
// which is strongest on Linux.
//
// On Windows a running executable cannot be replaced in place; the
// replacement moves it aside first. A crash between the two renames leaves
// the target path missing, and recovery is manual: rename
// "<exe>.go4updates.old" back to "<exe>". See the repository README for the
// full support matrix and recovery procedures.
//
// The backup is removed only by [Installer.Cleanup], which applications
// should call after successful initialization or a health check — reaching
// the new binary proves it starts, not that it is healthy. Cleanup is an
// artifact collector, not a rollback mechanism.
package selfupdate
