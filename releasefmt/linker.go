package releasefmt

import (
	"net/url"
	"strings"
)

// Linker resolves references found in release Markdown to provider URLs.
//
// Implementations report false for references they cannot resolve, in which
// case the Markdown transformation leaves the reference untouched. The
// linkers returned by this package are never nil and never fail to build:
// input that cannot be interpreted produces a no-op linker whose methods all
// report false.
type Linker interface {
	// CommitURL returns the browse URL of a full commit hash.
	CommitURL(sha string) (string, bool)

	// IssueURL returns the browse URL of an issue or pull request number.
	// The number is given without its leading "#".
	IssueURL(number string) (string, bool)

	// MentionURL returns the profile URL of a user mention. The username is
	// given without its leading "@".
	MentionURL(username string) (string, bool)
}

const (
	githubBaseURL = "https://github.com"
	gitlabBaseURL = "https://gitlab.com"
)

// GitHubLinker returns a Linker producing GitHub URLs for repository.
//
// repository is either an "owner/repository" pair, which is resolved against
// https://github.com, or an absolute http(s) URL of the repository, which
// also covers GitHub Enterprise hosts that serve the forge below a path
// prefix. Any other input, including an empty string, yields a no-op linker.
func GitHubLinker(repository string) Linker {
	return newRepoLinker(repository, githubBaseURL, false)
}

// ForgejoLinker returns a Linker producing Forgejo URLs for repository.
//
// Forgejo instances are self-hosted, so a bare "owner/repository" pair has no
// meaningful default server: repository must be an absolute http(s) URL of
// the repository, such as "https://forge.example.com/owner/repository". The
// URL shape is otherwise identical to GitHubLinker. Any other input, including
// a bare "owner/repository" pair, yields a no-op linker.
func ForgejoLinker(repository string) Linker {
	return newRepoLinker(repository, "", false)
}

// GitLabLinker returns a Linker producing GitLab URLs for repository.
//
// repository is either a project path such as "group/project" or
// "group/subgroup/project", which is resolved against https://gitlab.com, or
// an absolute http(s) URL of the project, whose whole path is treated as the
// project path so that nested groups keep working. Issue and commit links use
// the "/-/issues" and "/-/commit" shapes of modern GitLab. Any other input,
// including an empty string, yields a no-op linker.
func GitLabLinker(repository string) Linker {
	return newRepoLinker(repository, gitlabBaseURL, true)
}

// gitRefNamespace separates a GitLab repository path from its issue and
// commit paths.
const gitRefNamespace = "/-"

// repoLinker builds URLs for a single repository on a forge whose issue,
// commit, and profile pages follow the GitHub or GitLab layout. The zero
// value is a no-op linker.
type repoLinker struct {
	// base is the instance root user profiles hang off.
	base string

	// repoBase is base extended with the repository path. Issue and commit
	// links are built below it.
	repoBase string

	// refNamespace separates the repository path from issue and commit
	// paths. It is empty on GitHub-like hosts and gitRefNamespace on GitLab.
	refNamespace string
}

// CommitURL implements Linker.
func (l repoLinker) CommitURL(sha string) (string, bool) {
	if l.repoBase == "" || !isCommitSHA(sha) {
		return "", false
	}

	return l.repoBase + l.refNamespace + "/commit/" + sha, true
}

// IssueURL implements Linker.
func (l repoLinker) IssueURL(number string) (string, bool) {
	if l.repoBase == "" || !isIssueNumber(number) {
		return "", false
	}

	return l.repoBase + l.refNamespace + "/issues/" + number, true
}

// MentionURL implements Linker.
func (l repoLinker) MentionURL(username string) (string, bool) {
	if l.base == "" || !isMentionName(username) {
		return "", false
	}

	return l.base + "/" + username, true
}

// newRepoLinker builds a Linker for a repository reference on a forge whose
// default instance root is defaultBase.
//
// reference is either a bare repository path such as "owner/repository",
// which is resolved against defaultBase, or an absolute http(s) URL of the
// repository. When subgroups is true the whole path is taken as the
// repository path, which is how GitLab nested groups work; otherwise the last
// two path segments are taken as the owner and the repository and any
// remaining prefix is kept for every link. An empty defaultBase rejects bare
// repository paths, which is what a self-hosted forge needs.
func newRepoLinker(reference, defaultBase string, subgroups bool) repoLinker {
	reference = strings.TrimSpace(reference)

	if reference == "" {
		return repoLinker{}
	}

	if !strings.Contains(reference, "://") {
		if defaultBase == "" {
			return repoLinker{}
		}

		// A bare path names the repository exactly, subgroups included.
		segments := splitSegments(reference)
		if len(segments) < 2 || (!subgroups && len(segments) > 2) {
			return repoLinker{}
		}

		return newHostedLinker(defaultBase, segments, subgroups)
	}

	parsed, err := url.Parse(reference)
	if err != nil {
		return repoLinker{}
	}

	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return repoLinker{}
	}

	return newHostedLinker(parsed.Scheme+"://"+parsed.Host, splitSegments(parsed.EscapedPath()), subgroups)
}

// newHostedLinker turns an instance root and a repository path into a
// repoLinker, reporting a no-op linker when the path does not name a
// repository. Subgroup style paths are GitLab paths, which keep their nested
// groups in every link and use the "/-" namespace for issues and commits.
func newHostedLinker(base string, segments []string, subgroups bool) repoLinker {
	if len(segments) < 2 {
		return repoLinker{}
	}

	if subgroups {
		return repoLinker{
			base:         base,
			repoBase:     joinPath(base, segments),
			refNamespace: gitRefNamespace,
		}
	}

	repo := segments[len(segments)-2:]

	base = joinPath(base, segments[:len(segments)-2])

	return repoLinker{base: base, repoBase: joinPath(base, repo)}
}

// joinPath appends path segments to base, separated by single slashes.
func joinPath(base string, segments []string) string {
	if len(segments) == 0 {
		return base
	}

	return base + "/" + strings.Join(segments, "/")
}

// splitSegments splits a repository or URL path into its non-empty segments.
func splitSegments(path string) []string {
	parts := strings.Split(path, "/")

	segments := make([]string, 0, len(parts))

	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}

	return segments
}

// isCommitSHA reports whether s is a full hexadecimal commit hash.
func isCommitSHA(s string) bool {
	if len(s) != commitSHAHexLength {
		return false
	}

	for i := range len(s) {
		if !isHexDigit(s[i]) {
			return false
		}
	}

	return true
}

// isIssueNumber reports whether s is a bare decimal issue number.
func isIssueNumber(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}

	return true
}

// isMentionName reports whether s is a plausible user or group name.
func isMentionName(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if !isMentionByte(s[i]) {
			return false
		}
	}

	return true
}
