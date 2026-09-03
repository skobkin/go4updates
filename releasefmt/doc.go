// Package releasefmt turns raw upstream release Markdown into presentable
// output.
//
// The package is framework independent and performs presentation-oriented
// transformations only: [MarkdownBody] linkifies commit hashes, issue
// references, and user mentions inside a single Markdown document, and
// [Changelog] assembles several releases into one Markdown changelog. Raw
// release bodies are never modified; every function returns a new string and
// leaves its input untouched.
//
// Linkification is opt-in through a [Linker], with ready-made implementations
// for GitHub, Forgejo, and GitLab style hosts:
//
//	linker := releasefmt.GitHubLinker("acme/app")
//
//	changelog := releasefmt.Changelog(feed.Releases, releasefmt.Options{
//		Linker:         linker,
//		ShortCommitSHA: true,
//	})
//
// Transformations never touch fenced code blocks, inline code spans, links,
// images, autolinks, or link reference definitions. Existing Markdown
// therefore survives unchanged, and running [MarkdownBody] over its own
// output is a no-op.
package releasefmt
