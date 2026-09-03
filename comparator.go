package updates

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Comparator decides whether one version is older than another.
//
// Implementations receive the raw version strings and must not rely on the
// caller normalizing them. When versions cannot be compared, implementations
// must return StatusUnknown instead of guessing.
type Comparator interface {
	// Compare reports the status of current relative to latest.
	Compare(current, latest string) Status
}

// ComparatorFunc adapts a plain function to the Comparator interface.
type ComparatorFunc func(current, latest string) Status

// Compare implements Comparator.
func (f ComparatorFunc) Compare(current, latest string) Status {
	return f(current, latest)
}

// SemVerComparator compares semantic versions and is the default comparison
// policy.
//
// Both versions are accepted with or without a leading "v". Any invalid or
// empty version yields StatusUnknown. Current older than latest yields
// StatusUpdateAvailable; equal or newer versions yield StatusUpToDate.
// Build metadata is ignored and pre-release ordering follows the SemVer
// specification (for example 1.2.3-rc.1 is older than 1.2.3).
type SemVerComparator struct{}

// Compare implements Comparator.
func (SemVerComparator) Compare(current, latest string) Status {
	currentVersion := normalizeSemVer(current)
	latestVersion := normalizeSemVer(latest)

	if !semver.IsValid(currentVersion) || !semver.IsValid(latestVersion) {
		return StatusUnknown
	}

	if semver.Compare(currentVersion, latestVersion) < 0 {
		return StatusUpdateAvailable
	}

	return StatusUpToDate
}

// normalizeSemVer prepares a version string for comparison without altering
// the caller's value.
func normalizeSemVer(version string) string {
	version = strings.TrimSpace(version)

	if version == "" || version[0] == 'v' {
		return version
	}

	return "v" + version
}
