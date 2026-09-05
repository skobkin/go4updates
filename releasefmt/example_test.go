package releasefmt_test

import (
	"fmt"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/releasefmt"
)

// Changelog renders releases as "## version" sections whose bodies are the
// raw release Markdown run through [releasefmt.MarkdownBody], separated by
// horizontal rules. With a Linker, issue, commit, and mention references in
// the bodies become links; the releases themselves are not modified.
func ExampleChangelog() {
	releases := []updates.Release{
		{
			Version: "v1.1.0",
			Body:    "Adds the **watch** package.\n\nSee #12 for details, thanks @ada!",
		},
		{
			Version: "v1.0.1",
			Body:    "Fixes a nil dereference in the GitHub adapter.",
		},
		{
			Version: "v1.0.0",
			Body:    "",
		},
	}

	text := releasefmt.Changelog(releases, releasefmt.Options{
		Linker: releasefmt.GitHubLinker("https://github.com/example/example-app"),
	})

	fmt.Println(text)
	// Output:
	// ## v1.1.0
	//
	// Adds the **watch** package.
	//
	// See [#12](https://github.com/example/example-app/issues/12) for details, thanks [@ada](https://github.com/ada)!
	//
	// ---
	//
	// ## v1.0.1
	//
	// Fixes a nil dereference in the GitHub adapter.
	//
	// ---
	//
	// ## v1.0.0
}
