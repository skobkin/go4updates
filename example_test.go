package updates_test

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/source/github"
)

// A one-shot check needs only a source and a checker; the watch package is
// for periodic checking.
func ExampleChecker_Check() {
	src, err := github.New("example/example-app")
	if err != nil {
		log.Fatal(err)
	}

	checker := updates.NewChecker(updates.CheckerOptions{})

	result, err := checker.Check(context.Background(), updates.Target{
		CurrentVersion: "v1.4.0",
		Source:         src,
		Fetch: updates.FetchOptions{
			Limit: 10,
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	switch result.Status {
	case updates.StatusUpdateAvailable:
		fmt.Printf("update available: %s\n", result.Latest.Version)
	case updates.StatusUpToDate:
		fmt.Println("up to date")
	default:
		fmt.Println("unable to compare versions")
	}
}

// buildNumberComparator is an alternative comparison policy ordering builds
// by integer build number instead of semantic version.
type buildNumberComparator struct{}

func (buildNumberComparator) Compare(current, latest string) updates.Status {
	currentBuild, currentErr := strconv.Atoi(strings.TrimSpace(current))
	latestBuild, latestErr := strconv.Atoi(strings.TrimSpace(latest))

	// Build numbers that are missing or unreadable cannot be ordered.
	if currentErr != nil || latestErr != nil || currentBuild == latestBuild {
		return updates.StatusUnknown
	}

	if currentBuild < latestBuild {
		return updates.StatusUpdateAvailable
	}

	return updates.StatusUpToDate
}

// Any [updates.Comparator] can replace the default semantic-version policy,
// per checker or per target.
func Example_comparator() {
	var comparator updates.Comparator = buildNumberComparator{}

	fmt.Println(comparator.Compare("41", "42"))
	fmt.Println(comparator.Compare("42", "41"))
	fmt.Println(comparator.Compare("41", "not-a-number"))
	// Output:
	// update_available
	// up_to_date
	// unknown
}
