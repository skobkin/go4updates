package watch

import (
	updates "github.com/skobkin/go4updates"
)

// cloneEvent returns a deep copy of event. Subscribers hand out copies so
// that a consumer can never reach the watcher's internal state or the state
// another consumer was given.
func cloneEvent(event Event) Event {
	out := event
	out.State = cloneState(event.State)

	return out
}

// cloneState returns a deep copy of state.
func cloneState(state State) State {
	out := state
	out.Result = cloneResult(state.Result)

	return out
}

// cloneResult returns a deep copy of result, or nil when result is nil.
func cloneResult(result *updates.Result) *updates.Result {
	if result == nil {
		return nil
	}

	out := *result
	out.Feed = cloneFeed(result.Feed)

	if result.Latest != nil {
		latest := cloneRelease(*result.Latest)
		out.Latest = &latest
	}

	return &out
}

// cloneFeed returns a deep copy of feed.
func cloneFeed(feed updates.Feed) updates.Feed {
	out := feed
	out.Releases = cloneReleases(feed.Releases)

	return out
}

// cloneReleases returns a deep copy of releases, preserving a nil slice.
func cloneReleases(releases []updates.Release) []updates.Release {
	if releases == nil {
		return nil
	}

	out := make([]updates.Release, len(releases))
	for i := range releases {
		out[i] = cloneRelease(releases[i])
	}

	return out
}

// cloneRelease returns a deep copy of release.
func cloneRelease(release updates.Release) updates.Release {
	out := release
	out.Assets = cloneAssets(release.Assets)

	return out
}

// cloneAssets returns a deep copy of assets, preserving a nil slice.
func cloneAssets(assets []updates.Asset) []updates.Asset {
	if assets == nil {
		return nil
	}

	out := make([]updates.Asset, len(assets))
	copy(out, assets)

	return out
}
