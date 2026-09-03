// Package updates implements release checking for Go applications.
//
// A [Source] fetches a normalized release feed from an upstream hosting
// service. A [Checker] compares the application's current version with the
// newest release in that feed and produces a [Result]:
//
//	src, err := github.New("owner/repository")
//	if err != nil {
//		return err
//	}
//
//	checker := updates.NewChecker(updates.CheckerOptions{})
//
//	result, err := checker.Check(ctx, updates.Target{
//		CurrentVersion: "1.2.3",
//		Source:         src,
//	})
//
// Provider adapters live in the source subpackages (github, forgejo, gitlab).
// Periodic multi-target checking is provided by the watch package, and
// framework-independent release-note formatting by the releasefmt package.
//
// The package keeps responsibilities small: sources only fetch, the checker
// only compares a single fetch, and nothing here starts background work on
// the caller's behalf.
package updates
