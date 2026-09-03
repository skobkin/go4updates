package releasefmt

import "testing"

const (
	// testSHA is a full commit hash used across the formatting tests.
	testSHA = "0123456789abcdef0123456789abcdef01234567"

	// testShortSHA is the display form of testSHA.
	testShortSHA = "0123456789"

	// testSHA39 is testSHA without its last character.
	testSHA39 = "0123456789abcdef0123456789abcdef0123456"

	// testSHA41 is testSHA extended with one hexadecimal character.
	testSHA41 = "0123456789abcdef0123456789abcdef012345670"
)

// testGitHubLinker is the linker the formatting tests resolve references
// with.
func testGitHubLinker() Linker {
	return GitHubLinker("acme/app")
}

func TestGitHubLinkerURLs(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		issue      string
		commit     string
		mention    string
	}{
		{
			name:       "owner/repository pair",
			repository: "acme/app",
			issue:      "https://github.com/acme/app/issues/12",
			commit:     "https://github.com/acme/app/commit/" + testSHA,
			mention:    "https://github.com/alice",
		},
		{
			name:       "repository URL",
			repository: "https://github.com/acme/app",
			issue:      "https://github.com/acme/app/issues/12",
			commit:     "https://github.com/acme/app/commit/" + testSHA,
			mention:    "https://github.com/alice",
		},
		{
			name:       "repository URL with trailing slash",
			repository: "https://github.com/acme/app/",
			issue:      "https://github.com/acme/app/issues/12",
			commit:     "https://github.com/acme/app/commit/" + testSHA,
			mention:    "https://github.com/alice",
		},
		{
			name:       "enterprise host",
			repository: "https://git.example.com/acme/app",
			issue:      "https://git.example.com/acme/app/issues/12",
			commit:     "https://git.example.com/acme/app/commit/" + testSHA,
			mention:    "https://git.example.com/alice",
		},
		{
			name:       "enterprise host with path prefix",
			repository: "https://git.example.com/forge/acme/app",
			issue:      "https://git.example.com/forge/acme/app/issues/12",
			commit:     "https://git.example.com/forge/acme/app/commit/" + testSHA,
			mention:    "https://git.example.com/forge/alice",
		},
		{
			name:       "surrounding space is ignored",
			repository: "  acme/app  ",
			issue:      "https://github.com/acme/app/issues/12",
			commit:     "https://github.com/acme/app/commit/" + testSHA,
			mention:    "https://github.com/alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			linker := GitHubLinker(tt.repository)

			assertLinkerURLs(t, linker, tt.issue, tt.commit, tt.mention)
		})
	}
}

func TestForgejoLinkerURLs(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		issue      string
		commit     string
		mention    string
	}{
		{
			name:       "repository URL",
			repository: "https://forge.example.com/acme/app",
			issue:      "https://forge.example.com/acme/app/issues/12",
			commit:     "https://forge.example.com/acme/app/commit/" + testSHA,
			mention:    "https://forge.example.com/alice",
		},
		{
			name:       "http URL",
			repository: "http://forge.example.com/acme/app",
			issue:      "http://forge.example.com/acme/app/issues/12",
			commit:     "http://forge.example.com/acme/app/commit/" + testSHA,
			mention:    "http://forge.example.com/alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLinkerURLs(t, ForgejoLinker(tt.repository), tt.issue, tt.commit, tt.mention)
		})
	}
}

func TestGitLabLinkerURLs(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		issue      string
		commit     string
		mention    string
	}{
		{
			name:       "group and project",
			repository: "acme/app",
			issue:      "https://gitlab.com/acme/app/-/issues/12",
			commit:     "https://gitlab.com/acme/app/-/commit/" + testSHA,
			mention:    "https://gitlab.com/alice",
		},
		{
			name:       "nested subgroups",
			repository: "acme/team/app",
			issue:      "https://gitlab.com/acme/team/app/-/issues/12",
			commit:     "https://gitlab.com/acme/team/app/-/commit/" + testSHA,
			mention:    "https://gitlab.com/alice",
		},
		{
			name:       "self-hosted instance",
			repository: "https://git.example.com/acme/app",
			issue:      "https://git.example.com/acme/app/-/issues/12",
			commit:     "https://git.example.com/acme/app/-/commit/" + testSHA,
			mention:    "https://git.example.com/alice",
		},
		{
			name:       "self-hosted instance with subgroups",
			repository: "https://git.example.com/acme/team/app",
			issue:      "https://git.example.com/acme/team/app/-/issues/12",
			commit:     "https://git.example.com/acme/team/app/-/commit/" + testSHA,
			mention:    "https://git.example.com/alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLinkerURLs(t, GitLabLinker(tt.repository), tt.issue, tt.commit, tt.mention)
		})
	}
}

// assertLinkerURLs checks the issue, commit, and mention URLs of a linker.
func assertLinkerURLs(t *testing.T, linker Linker, issue, commit, mention string) {
	t.Helper()

	if got, ok := linker.IssueURL("12"); !ok || got != issue {
		t.Errorf("IssueURL(\"12\") = %q, %v; want %q, true", got, ok, issue)
	}

	if got, ok := linker.CommitURL(testSHA); !ok || got != commit {
		t.Errorf("CommitURL(%q) = %q, %v; want %q, true", testSHA, got, ok, commit)
	}

	if got, ok := linker.MentionURL("alice"); !ok || got != mention {
		t.Errorf("MentionURL(\"alice\") = %q, %v; want %q, true", got, ok, mention)
	}
}

func TestLinkerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		link  func() (string, bool)
		short string
	}{
		{
			name:  "empty reference",
			short: `GitHubLinker("").IssueURL("12")`,
			link:  func() (string, bool) { return GitHubLinker("").IssueURL("12") },
		},
		{
			name:  "single path segment",
			short: `GitHubLinker("acme").CommitURL(sha)`,
			link:  func() (string, bool) { return GitHubLinker("acme").CommitURL(testSHA) },
		},
		{
			name:  "three path segments",
			short: `GitHubLinker("acme/team/app").MentionURL("alice")`,
			link:  func() (string, bool) { return GitHubLinker("acme/team/app").MentionURL("alice") },
		},
		{
			name:  "relative URL",
			short: `GitHubLinker("./acme/app").IssueURL("12")`,
			link:  func() (string, bool) { return GitHubLinker("./acme/app").IssueURL("12") },
		},
		{
			name:  "unsupported scheme",
			short: `GitHubLinker("ssh://github.com/acme/app").CommitURL(sha)`,
			link: func() (string, bool) {
				return GitHubLinker("ssh://github.com/acme/app").CommitURL(testSHA)
			},
		},
		{
			name:  "URL without repository path",
			short: `GitHubLinker("https://github.com").IssueURL("12")`,
			link:  func() (string, bool) { return GitHubLinker("https://github.com").IssueURL("12") },
		},
		{
			name:  "URL without host",
			short: `GitHubLinker("https:///acme/app").CommitURL(sha)`,
			link:  func() (string, bool) { return GitHubLinker("https:///acme/app").CommitURL(testSHA) },
		},
		{
			name:  "unparsable URL",
			short: `GitHubLinker("https://git.example.com/acme/app%x")`,
			link: func() (string, bool) {
				return GitHubLinker("https://git.example.com/acme/app%x").MentionURL("alice")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := tt.link(); ok || got != "" {
				t.Errorf("%s = %q, %v; want \"\", false", tt.short, got, ok)
			}
		})
	}
}

func TestForgejoLinkerRejectsBareRepository(t *testing.T) {
	linker := ForgejoLinker("acme/app")

	if got, ok := linker.IssueURL("12"); ok || got != "" {
		t.Errorf("ForgejoLinker(\"acme/app\").IssueURL(\"12\") = %q, %v; want \"\", false", got, ok)
	}

	if got, ok := linker.MentionURL("alice"); ok || got != "" {
		t.Errorf("ForgejoLinker(\"acme/app\").MentionURL(\"alice\") = %q, %v; want \"\", false", got, ok)
	}
}

func TestLinkerRejectsInvalidReferenceValues(t *testing.T) {
	linker := testGitHubLinker()

	tests := []struct {
		name  string
		short string
		link  func() (string, bool)
	}{
		{
			name:  "short commit hash",
			short: "CommitURL(testSHA39)",
			link:  func() (string, bool) { return linker.CommitURL(testSHA39) },
		},
		{
			name:  "commit hash with a word character",
			short: "CommitURL(sha + \"g\")",
			link:  func() (string, bool) { return linker.CommitURL(testSHA + "g") },
		},
		{
			name:  "non-hex commit hash",
			short: "CommitURL(\"zzzz\")",
			link:  func() (string, bool) { return linker.CommitURL("zzzz") },
		},
		{
			name:  "empty issue number",
			short: "IssueURL(\"\")",
			link:  func() (string, bool) { return linker.IssueURL("") },
		},
		{
			name:  "issue number with prefix",
			short: "IssueURL(\"#12\")",
			link:  func() (string, bool) { return linker.IssueURL("#12") },
		},
		{
			name:  "empty username",
			short: "MentionURL(\"\")",
			link:  func() (string, bool) { return linker.MentionURL("") },
		},
		{
			name:  "username with slash",
			short: "MentionURL(\"acme/alice\")",
			link:  func() (string, bool) { return linker.MentionURL("acme/alice") },
		},
		{
			name:  "username with query",
			short: "MentionURL(\"alice?tab=profile\")",
			link:  func() (string, bool) { return linker.MentionURL("alice?tab=profile") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := tt.link(); ok || got != "" {
				t.Errorf("%s = %q, %v; want \"\", false", tt.short, got, ok)
			}
		})
	}
}

func TestLinkersAreUsableThroughTheInterface(t *testing.T) {
	linkers := map[string]Linker{
		"github":  GitHubLinker("acme/app"),
		"forgejo": ForgejoLinker("https://forge.example.com/acme/app"),
		"gitlab":  GitLabLinker("acme/app"),
	}

	for name, linker := range linkers {
		t.Run(name, func(t *testing.T) {
			if _, ok := linker.CommitURL(testSHA); !ok {
				t.Errorf("%s linker did not resolve commit %q", name, testSHA)
			}

			if _, ok := linker.IssueURL("1"); !ok {
				t.Errorf("%s linker did not resolve issue 1", name)
			}

			if _, ok := linker.MentionURL("alice"); !ok {
				t.Errorf("%s linker did not resolve mention alice", name)
			}
		})
	}
}
