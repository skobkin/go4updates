//go:build race

package markdown

// raceDetector reports whether the tests are compiled with the race
// detector, which is the only way to skip tests from within them.
const raceDetector = true
