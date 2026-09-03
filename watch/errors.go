package watch

import "errors"

// ErrEmptyID is returned by Watcher.Add when the target id is empty.
var ErrEmptyID = errors.New("go4updates: watch: target id must not be empty")

// ErrDuplicateID is returned by Watcher.Add when a target with the same id is
// already registered.
var ErrDuplicateID = errors.New("go4updates: watch: target id already registered")

// ErrWatcherStarted is returned when the watcher has already been started
// once. It is reported by Watcher.Add called after Watcher.Run and by a
// second Watcher.Run call: a watcher is single use and its lifecycle is
// configured -> running -> finished.
var ErrWatcherStarted = errors.New("go4updates: watch: watcher has already been started")
