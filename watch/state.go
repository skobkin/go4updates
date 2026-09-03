package watch

import (
	"time"

	updates "github.com/skobkin/go4updates"
)

// State is the last known state of a single watched target.
//
// After a failed attempt the state keeps the last successful Result and only
// refreshes LastAttemptAt and LastError. Stale but useful data therefore
// stays distinguishable from the data of a fresh successful check.
type State struct {
	// Result is the outcome of the last successful check, or nil when no
	// check of the target has succeeded yet.
	Result *updates.Result

	// LastAttemptAt is when the last attempt completed, successfully or not.
	LastAttemptAt time.Time

	// LastError is the failure of the last attempt, or nil when the attempt
	// succeeded.
	LastError error
}

// State returns the last known state of the target registered under id.
//
// The second result reports that the target has completed at least one check;
// it is false for unknown ids and for targets that have not been checked yet.
//
// The returned State is a deep copy. Mutating it never affects the watcher,
// other callers, or delivered events.
func (w *Watcher) State(id string) (State, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	state, ok := w.states[id]
	if !ok {
		return State{}, false
	}

	return cloneState(state), true
}

// States returns a snapshot of the last known state of every target.
//
// The map and every State in it are deep copies. Mutating them never affects
// the watcher, other callers, or delivered events. The map is empty, not nil,
// when no target has been checked yet.
func (w *Watcher) States() map[string]State {
	w.mu.RLock()
	defer w.mu.RUnlock()

	out := make(map[string]State, len(w.states))
	for id, state := range w.states {
		out[id] = cloneState(state)
	}

	return out
}
