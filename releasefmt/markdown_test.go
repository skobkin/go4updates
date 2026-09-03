package releasefmt

import (
	"strings"
	"testing"
)

const (
	// testCommitURL is the URL testGitHubLinker builds for testSHA.
	testCommitURL = "https://github.com/acme/app/commit/" + testSHA

	// testSHADecimal is a full length hash made of decimal digits only, so
	// that it could be mistaken for an issue number.
	testSHADecimal = "1234567890123456789012345678901234567890"
)

// testIssueLink builds the link the test linker produces for an issue.
func testIssueLink(number string) string {
	return "[#" + number + "](https://github.com/acme/app/issues/" + number + ")"
}

// testMentionLink builds the link the test linker produces for a mention.
func testMentionLink(username string) string {
	return "[@" + username + "](https://github.com/" + username + ")"
}

func TestMarkdownBodyCommitHashes(t *testing.T) {
	linked := Options{Linker: testGitHubLinker()}
	short := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}
	plain := Options{}

	shortLink := "[" + testShortSHA + "](" + testCommitURL + ")"

	tests := []struct {
		name string
		body string
		want string
		opts Options
	}{
		{
			name: "text without references is unchanged",
			body: "Fixed a crash in the settings dialog.",
			want: "Fixed a crash in the settings dialog.",
			opts: linked,
		},
		{
			name: "hash in a sentence is linked",
			body: "Fixed in " + testSHA + " thanks everybody.",
			want: "Fixed in [" + testSHA + "](" + testCommitURL + ") thanks everybody.",
			opts: linked,
		},
		{
			name: "hash at line start is linked",
			body: testSHA + " fixes the crash",
			want: "[" + testSHA + "](" + testCommitURL + ") fixes the crash",
			opts: linked,
		},
		{
			name: "hash at line end is linked",
			body: "see " + testSHA,
			want: "see [" + testSHA + "](" + testCommitURL + ")",
			opts: linked,
		},
		{
			name: "hash in parentheses is linked",
			body: "(" + testSHA + ")",
			want: "([" + testSHA + "](" + testCommitURL + "))",
			opts: linked,
		},
		{
			name: "hash followed by punctuation is linked",
			body: testSHA + ", " + testSHA + ".",
			want: "[" + testSHA + "](" + testCommitURL + "), [" + testSHA + "](" + testCommitURL + ").",
			opts: linked,
		},
		{
			name: "two hashes on one line are both linked",
			body: testSHA + " reverted " + testSHA,
			want: "[" + testSHA + "](" + testCommitURL + ") reverted [" + testSHA + "](" + testCommitURL + ")",
			opts: linked,
		},
		{
			name: "hashes on several lines are all linked",
			body: testSHA + "\n" + testSHA,
			want: "[" + testSHA + "](" + testCommitURL + ")\n[" + testSHA + "](" + testCommitURL + ")",
			opts: linked,
		},
		{
			name: "short display keeps the link",
			body: "Fixed in " + testSHA,
			want: "Fixed in [" + testShortSHA + "](" + testCommitURL + ")",
			opts: short,
		},
		{
			name: "short display without a linker keeps the hash",
			body: "Fixed in " + testSHA,
			want: "Fixed in " + testShortSHA,
			opts: Options{ShortCommitSHA: true},
		},
		{
			name: "full display without a linker is unchanged",
			body: "Fixed in " + testSHA,
			want: "Fixed in " + testSHA,
			opts: plain,
		},
		{
			name: "uppercase hash is linked",
			body: "Fixed in " + testSHA[:32] + "ABCDEF01",
			want: "Fixed in [" + testSHA[:32] + "ABCDEF01" + "](https://github.com/acme/app/commit/" + testSHA[:32] + "ABCDEF01)",
			opts: linked,
		},
		{
			name: "39 hexadecimal characters are untouched",
			body: "partial " + testSHA39,
			want: "partial " + testSHA39,
			opts: short,
		},
		{
			name: "41 hexadecimal characters are untouched",
			body: "long " + testSHA41,
			want: "long " + testSHA41,
			opts: short,
		},
		{
			name: "longer hexadecimal run is untouched",
			body: "run " + testSHA + testSHA,
			want: "run " + testSHA + testSHA,
			opts: short,
		},
		{
			name: "hash inside a word is untouched",
			body: "prefix" + testSHA + "suffix",
			want: "prefix" + testSHA + "suffix",
			opts: short,
		},
		{
			name: "hash preceded by an underscore is untouched",
			body: "_" + testSHA,
			want: "_" + testSHA,
			opts: short,
		},
		{
			name: "hash inside inline code is untouched",
			body: "run `git checkout " + testSHA + "` first",
			want: "run `git checkout " + testSHA + "` first",
			opts: short,
		},
		{
			name: "hash inside a double backtick code span is untouched",
			body: "run ``git checkout " + testSHA + "`` next",
			want: "run ``git checkout " + testSHA + "`` next",
			opts: short,
		},
		{
			name: "hash inside link text is untouched",
			body: "[see " + testSHA + " for details](https://example.com)",
			want: "[see " + testSHA + " for details](https://example.com)",
			opts: short,
		},
		{
			name: "hash inside a link destination is untouched",
			body: "[compare](https://example.com/compare/" + testSHA + "..main)",
			want: "[compare](https://example.com/compare/" + testSHA + "..main)",
			opts: short,
		},
		{
			name: "hash inside an image is untouched",
			body: "![build " + testSHA + "](https://example.com/badge.png)",
			want: "![build " + testSHA + "](https://example.com/badge.png)",
			opts: short,
		},
		{
			name: "hash inside an autolink is untouched",
			body: "<https://example.com/commit/" + testSHA + ">",
			want: "<https://example.com/commit/" + testSHA + ">",
			opts: short,
		},
		{
			name: "hash inside a link reference definition is untouched",
			body: "[" + testSHA + "]: https://example.com/commit/" + testSHA,
			want: "[" + testSHA + "]: https://example.com/commit/" + testSHA,
			opts: short,
		},
		{
			name: "hash inside a backtick fence is untouched",
			body: "```\ncommit " + testSHA + "\n```",
			want: "```\ncommit " + testSHA + "\n```",
			opts: short,
		},
		{
			name: "hash inside a tilde fence is untouched",
			body: "~~~\ncommit " + testSHA + "\n~~~",
			want: "~~~\ncommit " + testSHA + "\n~~~",
			opts: short,
		},
		{
			name: "hash inside an indented fence with language is untouched",
			body: "before " + testSHA + "\n\n  ```go\nfmt.Println(\"" + testSHA + "\")\n  ```\n\nafter " + testSHA,
			want: "before " + shortLink + "\n\n  ```go\nfmt.Println(\"" + testSHA + "\")\n  ```\n\nafter " + shortLink,
			opts: short,
		},
		{
			name: "hash inside an unclosed fence is untouched",
			body: "```\ncommit " + testSHA,
			want: "```\ncommit " + testSHA,
			opts: short,
		},
		{
			name: "hash after a fence closes is linked",
			body: "```\n" + testSHA + "\n```\ncommit " + testSHA,
			want: "```\n" + testSHA + "\n```\ncommit " + shortLink,
			opts: short,
		},
		{
			name: "unclosed code span does not protect the rest of the line",
			body: "a ` b " + testSHA,
			want: "a ` b " + shortLink,
			opts: short,
		},
		{
			name: "carriage returns survive",
			body: "commit " + testSHA + "\r\nnext " + testSHA + "\r\n",
			want: "commit [" + testSHA + "](" + testCommitURL + ")\r\nnext [" + testSHA + "](" + testCommitURL + ")\r\n",
			opts: linked,
		},
		{
			name: "empty body stays empty",
			body: "",
			want: "",
			opts: short,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownBody(tt.body, tt.opts)
			if got != tt.want {
				t.Errorf("MarkdownBody() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestMarkdownBodyIssueReferences(t *testing.T) {
	linked := Options{Linker: testGitHubLinker()}
	short := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}
	bare := Options{ShortCommitSHA: true}

	shortLink := "[" + testShortSHA + "](" + testCommitURL + ")"

	tests := []struct {
		name string
		body string
		want string
		opts Options
	}{
		{
			name: "reference at line start is linked",
			body: "#12 fixes it",
			want: testIssueLink("12") + " fixes it",
			opts: linked,
		},
		{
			name: "reference in a sentence is linked",
			body: "fixes #12",
			want: "fixes " + testIssueLink("12"),
			opts: linked,
		},
		{
			name: "reference in parentheses is linked",
			body: "done (#12)",
			want: "done (" + testIssueLink("12") + ")",
			opts: linked,
		},
		{
			name: "reference after a word is linked",
			body: "fixes bug#12",
			want: "fixes bug" + testIssueLink("12"),
			opts: linked,
		},
		{
			name: "two adjacent references are both linked",
			body: "#12#13",
			want: testIssueLink("12") + testIssueLink("13"),
			opts: linked,
		},
		{
			name: "reference inside a path is untouched",
			body: "see path/#12",
			want: "see path/#12",
			opts: linked,
		},
		{
			name: "reference inside a URL is untouched",
			body: "open https://example.com/issues:#12",
			want: "open https://example.com/issues:#12",
			opts: linked,
		},
		{
			name: "reference after a backslash is untouched",
			body: `escaped \#12`,
			want: `escaped \#12`,
			opts: linked,
		},
		{
			name: "reference inside inline code is untouched",
			body: "see `#12`",
			want: "see `#12`",
			opts: linked,
		},
		{
			name: "reference inside a link is untouched",
			body: "[issue #12](https://example.com/12)",
			want: "[issue #12](https://example.com/12)",
			opts: linked,
		},
		{
			name: "reference inside a fence is untouched",
			body: "```\nfix #12\n```",
			want: "```\nfix #12\n```",
			opts: linked,
		},
		{
			name: "reference without digits is untouched",
			body: "issue #abc and lone #",
			want: "issue #abc and lone #",
			opts: linked,
		},
		{
			name: "reference keeps surrounding words",
			body: "#12abc",
			want: testIssueLink("12") + "abc",
			opts: linked,
		},
		{
			name: "hash before a full hash is not an issue",
			body: "#" + testSHADecimal,
			want: "#[" + testSHADecimal + "](https://github.com/acme/app/commit/" + testSHADecimal + ")",
			opts: linked,
		},
		{
			name: "hash with letters is not split into an issue",
			body: "#" + testSHA,
			want: "#" + shortLink,
			opts: short,
		},
		{
			name: "reference without a linker is untouched",
			body: "fixes #12",
			want: "fixes #12",
			opts: bare,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownBody(tt.body, tt.opts)
			if got != tt.want {
				t.Errorf("MarkdownBody() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestMarkdownBodyMentions(t *testing.T) {
	linked := Options{Linker: testGitHubLinker()}

	tests := []struct {
		name string
		body string
		want string
		opts Options
	}{
		{
			name: "mention at line start is linked",
			body: "@alice fixed it",
			want: testMentionLink("alice") + " fixed it",
			opts: linked,
		},
		{
			name: "mention after whitespace is linked",
			body: "thanks @alice!",
			want: "thanks " + testMentionLink("alice") + "!",
			opts: linked,
		},
		{
			name: "mention after punctuation is untouched",
			body: "reviewed by (@alice)",
			want: "reviewed by (@alice)",
			opts: linked,
		},
		{
			name: "mention with underscores and dots is linked",
			body: "thanks @alice_b.smith-2",
			want: "thanks " + testMentionLink("alice_b.smith-2"),
			opts: linked,
		},
		{
			name: "trailing dot is not part of a mention",
			body: "thanks @alice.",
			want: "thanks " + testMentionLink("alice") + ".",
			opts: linked,
		},
		{
			name: "two mentions are both linked",
			body: "@alice and @bob",
			want: testMentionLink("alice") + " and " + testMentionLink("bob"),
			opts: linked,
		},
		{
			name: "e-mail address is untouched",
			body: "contact alice@example.com",
			want: "contact alice@example.com",
			opts: linked,
		},
		{
			name: "mention without a name is untouched",
			body: "at @ and @ alone",
			want: "at @ and @ alone",
			opts: linked,
		},
		{
			name: "mention followed by another at sign is linked once",
			body: "@alice@host",
			want: testMentionLink("alice") + "@host",
			opts: linked,
		},
		{
			name: "mention inside inline code is untouched",
			body: "ping `@alice`",
			want: "ping `@alice`",
			opts: linked,
		},
		{
			name: "mention inside a link is untouched",
			body: "[@alice](https://example.com/alice)",
			want: "[@alice](https://example.com/alice)",
			opts: linked,
		},
		{
			name: "mention inside a fence is untouched",
			body: "```\ncc @alice\n```",
			want: "```\ncc @alice\n```",
			opts: linked,
		},
		{
			name: "mention after a word is untouched",
			body: "mail bob@alice",
			want: "mail bob@alice",
			opts: linked,
		},
		{
			name: "mention without a linker is untouched",
			body: "thanks @alice",
			want: "thanks @alice",
			opts: Options{ShortCommitSHA: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownBody(tt.body, tt.opts)
			if got != tt.want {
				t.Errorf("MarkdownBody() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestMarkdownBodyWithNoOpLinker(t *testing.T) {
	body := "Fixed in " + testSHA + " (#12) by @alice"
	shortened := "Fixed in " + testShortSHA + " (#12) by @alice"

	tests := []struct {
		name string
		link Linker
		opts Options
		want string
	}{
		{
			name: "empty repository",
			link: GitHubLinker(""),
			opts: Options{ShortCommitSHA: true},
			want: shortened,
		},
		{
			name: "single path segment",
			link: GitHubLinker("acme"),
			opts: Options{},
			want: body,
		},
		{
			name: "unsupported scheme",
			link: GitHubLinker("ssh://github.com/acme/app"),
			opts: Options{},
			want: body,
		},
		{
			name: "bare forgejo repository",
			link: ForgejoLinker("acme/app"),
			opts: Options{},
			want: body,
		},
		{
			name: "short display without a linker",
			link: nil,
			opts: Options{ShortCommitSHA: true},
			want: shortened,
		},
		{
			name: "no options at all",
			link: nil,
			opts: Options{},
			want: body,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownBody(body, Options{Linker: tt.link, ShortCommitSHA: tt.opts.ShortCommitSHA})
			if got != tt.want {
				t.Errorf("MarkdownBody() =\n%q\nwant\n%q", got, tt.want)
			}

			if strings.Contains(got, "](") {
				t.Errorf("MarkdownBody() =\n%s\nwant it to contain no links", got)
			}
		})
	}
}

func TestMarkdownBodyScannerEdges(t *testing.T) {
	opts := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}

	shortLink := "[" + testShortSHA + "](" + testCommitURL + ")"

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "nested angle brackets still protect the text",
			body: "see <a <" + testSHA + "> here",
			want: "see <a <" + testSHA + "> here",
		},
		{
			name: "unclosed angle bracket does not protect the rest",
			body: "<" + testSHA,
			want: "<" + shortLink,
		},
		{
			name: "whitespace inside angle brackets does not protect",
			body: "<see " + testSHA + " now>",
			want: "<see " + shortLink + " now>",
		},
		{
			name: "unclosed link destination does not protect",
			body: "[x](" + testSHA,
			want: "[x](" + shortLink,
		},
		{
			name: "unclosed link text does not protect",
			body: "[see " + testSHA,
			want: "[see " + shortLink,
		},
		{
			name: "nested parentheses in a destination are protected",
			body: "[nested](https://example.com/a(" + testSHA + "))",
			want: "[nested](https://example.com/a(" + testSHA + "))",
		},
		{
			name: "mismatched backtick runs do not protect",
			body: "`` x` " + testSHA,
			want: "`` x` " + shortLink,
		},
		{
			name: "indented link reference definition is protected",
			body: "\t[" + testSHA + "]: https://example.com/" + testSHA,
			want: "\t[" + testSHA + "]: https://example.com/" + testSHA,
		},
		{
			name: "definition without a destination is not a definition",
			body: "[ref]:",
			want: "[ref]:",
		},
		{
			name: "four space indent is not a fence",
			body: "    ```\n" + testSHA,
			want: "    ```\n" + shortLink,
		},
		{
			name: "tab inside angle brackets does not protect",
			body: "<a\t" + testSHA + ">",
			want: "<a\t" + shortLink + ">",
		},
		{
			name: "unclosed outer bracket is not a link",
			body: "[outer [inner](https://example.com)] " + testSHA,
			want: "[outer [inner](https://example.com)] " + shortLink,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownBody(tt.body, opts)
			if got != tt.want {
				t.Errorf("MarkdownBody() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestMarkdownBodyMixedDocument(t *testing.T) {
	linker := testGitHubLinker()

	body := "## Highlights\n\n" +
		"- fix crash (#12)\n" +
		"- update deps by @alice\n" +
		"- rework sync, see " + testSHA + "\n" +
		"\n" +
		"```bash\n" +
		"go install example.com/app@" + testSHA + "\n" +
		"```\n" +
		"\n" +
		"Full changelog: [v1.2.0](https://example.com/compare/" + testSHA + "...v1.2.0)\n"

	want := "## Highlights\n\n" +
		"- fix crash (" + testIssueLink("12") + ")\n" +
		"- update deps by " + testMentionLink("alice") + "\n" +
		"- rework sync, see [" + testShortSHA + "](" + testCommitURL + ")\n" +
		"\n" +
		"```bash\n" +
		"go install example.com/app@" + testSHA + "\n" +
		"```\n" +
		"\n" +
		"Full changelog: [v1.2.0](https://example.com/compare/" + testSHA + "...v1.2.0)\n"

	got := MarkdownBody(body, Options{Linker: linker, ShortCommitSHA: true})
	if got != want {
		t.Errorf("MarkdownBody() =\n%s\nwant\n%s", got, want)
	}
}

func TestMarkdownBodyIsIdempotent(t *testing.T) {
	opts := Options{Linker: testGitHubLinker(), ShortCommitSHA: true}

	body := "Fixed in " + testSHA + " (#12), thanks @alice.\n\n" +
		"```go\nfmt.Println(\"" + testSHA + "\")\n```\n\n" +
		"[compare](https://example.com/" + testSHA + ") and ![badge](https://example.com/b.png)\n"

	once := MarkdownBody(body, opts)
	twice := MarkdownBody(once, opts)

	if once != twice {
		t.Errorf("MarkdownBody is not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}
