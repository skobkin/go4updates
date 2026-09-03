package releasefmt

import (
	"strings"

	updates "github.com/skobkin/go4updates"
)

const (
	// sectionHeadingPrefix starts every changelog section.
	sectionHeadingPrefix = "## "

	// sectionSeparator separates two changelog sections.
	sectionSeparator = "\n\n---\n\n"
)

// Changelog builds a Markdown changelog from releases.
//
// Releases are rendered in the order they are given: a feed is already
// newest-first, so no second ordering policy is applied here and a caller
// passing releases in another order keeps that order.
//
// Every release becomes a "## version" section whose heading uses the release
// version verbatim and whose body is the raw release body run through
// [MarkdownBody]. Surrounding space is trimmed from the body, so a release
// with an empty body contributes a heading only. Sections are separated by a
// horizontal rule and an empty releases slice yields an empty changelog.
//
// The releases are only read: the raw bodies of the given releases are left
// untouched.
func Changelog(releases []updates.Release, opts Options) string {
	if len(releases) == 0 {
		return ""
	}

	sections := make([]string, 0, len(releases))

	for _, release := range releases {
		sections = append(sections, changelogSection(release, opts))
	}

	return strings.Join(sections, sectionSeparator)
}

// changelogSection renders a single release as one changelog section.
func changelogSection(release updates.Release, opts Options) string {
	section := sectionHeadingPrefix + release.Version

	body := strings.TrimSpace(MarkdownBody(release.Body, opts))
	if body == "" {
		return section
	}

	return section + "\n\n" + body
}
