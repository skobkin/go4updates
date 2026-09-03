package releasefmt

import (
	"strings"
	"testing"

	updates "github.com/skobkin/go4updates"
)

func TestChangelogPreservesReleaseOrder(t *testing.T) {
	releases := []updates.Release{
		{Version: "v1.0.0", Body: "initial release"},
		{Version: "v1.1.0", Body: "oldest change"},
		{Version: "v2.0.0", Body: "newest change"},
	}

	want := "## v1.0.0\n\ninitial release" +
		"\n\n---\n\n" +
		"## v1.1.0\n\noldest change" +
		"\n\n---\n\n" +
		"## v2.0.0\n\nnewest change"

	if got := Changelog(releases, Options{}); got != want {
		t.Errorf("Changelog() =\n%s\nwant\n%s", got, want)
	}
}

func TestChangelogKeepsNewestFirstFeedOrder(t *testing.T) {
	feed := updates.Feed{
		RepositoryURL: "https://github.com/acme/app",
		Releases: []updates.Release{
			{Version: "v1.2.0", Body: "latest"},
			{Version: "v1.1.0", Body: "middle"},
			{Version: "v1.0.0", Body: "first"},
		},
	}

	got := Changelog(feed.Releases, Options{})

	want := "## v1.2.0\n\nlatest\n\n---\n\n## v1.1.0\n\nmiddle\n\n---\n\n## v1.0.0\n\nfirst"
	if got != want {
		t.Errorf("Changelog() =\n%s\nwant\n%s", got, want)
	}
}

func TestChangelogFormatsBodies(t *testing.T) {
	opts := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}

	releases := []updates.Release{
		{
			Version: "v1.2.0",
			Body:    "Fixed in " + testSHA + " by @alice (#12)",
		},
	}

	want := "## v1.2.0\n\nFixed in [" + testShortSHA + "](" + testCommitURL + ") by " +
		testMentionLink("alice") + " (" + testIssueLink("12") + ")"

	if got := Changelog(releases, opts); got != want {
		t.Errorf("Changelog() =\n%s\nwant\n%s", got, want)
	}
}

func TestChangelogSectionShapes(t *testing.T) {
	tests := []struct {
		name     string
		releases []updates.Release
		want     string
	}{
		{
			name:     "empty releases",
			releases: nil,
			want:     "",
		},
		{
			name: "empty body yields a heading only",
			releases: []updates.Release{
				{Version: "v1.0.0"},
				{Version: "v1.1.0", Body: "change"},
			},
			want: "## v1.0.0\n\n---\n\n## v1.1.0\n\nchange",
		},
		{
			name: "blank body yields a heading only",
			releases: []updates.Release{
				{Version: "v1.0.0", Body: "\n\n   \n"},
			},
			want: "## v1.0.0",
		},
		{
			name: "surrounding space is trimmed from a body",
			releases: []updates.Release{
				{Version: "v1.0.0", Body: "\n\nchange\n\n"},
			},
			want: "## v1.0.0\n\nchange",
		},
		{
			name: "version is used verbatim",
			releases: []updates.Release{
				{Version: "1.2.3", Body: "change"},
			},
			want: "## 1.2.3\n\nchange",
		},
		{
			name: "no separator after the last section",
			releases: []updates.Release{
				{Version: "v1.0.0", Body: "change"},
			},
			want: "## v1.0.0\n\nchange",
		},
		{
			name: "bodies keep their internal blank lines",
			releases: []updates.Release{
				{Version: "v1.0.0", Body: "first\n\nsecond"},
			},
			want: "## v1.0.0\n\nfirst\n\nsecond",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Changelog(tt.releases, Options{}); got != tt.want {
				t.Errorf("Changelog() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestChangelogDoesNotMutateReleases(t *testing.T) {
	bodies := []string{
		"Fixed in " + testSHA + " (#12) by @alice",
		"```\n" + testSHA + "\n```\n\n[link](https://example.com/" + testSHA + ")",
	}

	releases := []updates.Release{
		{Version: "v1.1.0", Body: bodies[0]},
		{Version: "v1.0.0", Body: bodies[1]},
	}

	Changelog(releases, Options{Linker: testGitHubLinker(), ShortCommitSHA: true})

	for i, release := range releases {
		if release.Body != bodies[i] {
			t.Errorf("release %d body changed to %q, want %q", i, release.Body, bodies[i])
		}

		if release.Version == "" {
			t.Errorf("release %d lost its version", i)
		}
	}
}

func TestChangelogOutputIsStable(t *testing.T) {
	opts := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}

	releases := []updates.Release{
		{Version: "v1.1.0", Body: "Fixed in " + testSHA + " (#12) by @alice"},
		{Version: "v1.0.0", Body: "```\n" + testSHA + "\n```"},
	}

	once := Changelog(releases, opts)
	twice := MarkdownBody(once, opts)

	if once != twice {
		t.Errorf("MarkdownBody changed the changelog:\nbefore:\n%s\nafter:\n%s", once, twice)
	}

	if !strings.Contains(once, "["+testShortSHA+"]("+testCommitURL+")") {
		t.Errorf("Changelog() =\n%s\nwant it to contain a linked commit", once)
	}
}
