package selfupdate

import (
	"context"
	"io"
	"time"
)

// Stage identifies one phase of the update pipeline reported through
// [Progress]. Stages are reported in pipeline order; a stage whose step is
// disabled emits no events (for example [StageVerifying] when the Updater
// was built with [WithInsecureSkipVerify]).
type Stage uint8

const (
	// StageDownloading covers streaming the release asset into a staging
	// file. Byte events carry the server's Content-Length as Total when the
	// server declares one, and Total of zero when it does not.
	StageDownloading Stage = iota

	// StageVerifying covers validating the downloaded bytes. It is the last
	// stage whose cancellation is guaranteed to leave the installed
	// executable and all rollback material untouched. [StageInstalling] is
	// the boundary after which a UI must treat the update as committed.
	StageVerifying

	// StageExtracting covers streaming the executable out of the downloaded
	// asset into the executable staging file. Byte events carry the archive
	// member's declared size as Total when the format declares one (zip
	// members, tar members), and Total of zero when it does not (gzip and
	// raw assets).
	StageExtracting

	// StageInstalling covers the atomic replacement of the target. From
	// this event on, a canceled context may still abort cleanly before the
	// first rename, but the replacement may also already have taken effect
	// (an error carrying [ErrReplaced]); UIs must stop offering
	// cancellation when this stage is reported, and the outcome is read
	// from the returned error, never from progress events.
	StageInstalling

	// StageCompleted is reported exactly once, after the whole pipeline
	// succeeded and [Updater.ApplyWithProgress] is about to return a nil
	// error. It is never reported for a failed pipeline.
	StageCompleted
)

// String returns the stage name, for logs and CLI displays.
func (s Stage) String() string {
	switch s {
	case StageDownloading:
		return "downloading"

	case StageVerifying:
		return "verifying"

	case StageExtracting:
		return "extracting"

	case StageInstalling:
		return "installing"

	case StageCompleted:
		return "completed"

	default:
		return "unknown"
	}
}

// Progress is one progress observation delivered to the
// [Updater.ApplyWithProgress] callback.
//
// Current and Total are byte counts for the byte-carrying stages
// ([StageDownloading], [StageVerifying], [StageExtracting]) and zero for
// [StageInstalling] and [StageCompleted]. A Total of zero means the total
// is unknown: consumers must not derive percentages or ETAs from it. A
// stage is determinate once any of its events carries Total greater than
// zero, and indeterminate otherwise — switch the progress display on that,
// not on guesses about the verifier or the server. A download Total may be
// refined from zero once the response headers arrive and stays constant
// within the stage afterwards.
type Progress struct {
	// Stage is the pipeline phase this observation belongs to.
	Stage Stage

	// AssetName is the release asset file name being processed, on every
	// event of the pipeline.
	AssetName string

	// Current is the number of bytes processed so far within the stage.
	// Byte counts are monotonic within a stage.
	Current int64

	// Total is the stage's total byte count when known, and zero when
	// unknown. It never denotes completion of an unknown-size stage: a
	// final event after a verifier that consumed an unknown amount of work
	// still carries Total of zero.
	Total int64
}

// progressFunc receives an intermediate byte observation. current is
// monotonic within a stage; total is zero when unknown.
type progressFunc func(current, total int64)

// defaultProgressInterval is the minimum time between intermediate byte
// events within one stage. Stage transitions and per-stage final events
// bypass it entirely.
const defaultProgressInterval = 150 * time.Millisecond

// progressEmitter turns pipeline byte observations into throttled
// [Progress] callback events. It is created per Apply pipeline (never
// shared across goroutines) and no-ops entirely when the callback is nil.
//
// Guarantees, independent of throttling:
//
//   - stage transitions are emitted immediately;
//   - the first byte observation of a stage is emitted immediately, so a
//     known Total reaches the consumer before the throttling window opens;
//   - the final event of a successfully completed stage is emitted
//     immediately and carries the last observed byte counts exactly;
//   - intermediate events are suppressed while the byte count is unchanged
//     and until minInterval has elapsed since the last emitted event.
type progressEmitter struct {
	cb          func(Progress)
	assetName   string
	minInterval time.Duration
	now         func() time.Time

	stage Stage

	// observed and observedTotal track the latest byte counts of the
	// current stage, so the final event can report exact counts even when
	// intermediate events were throttled away.
	observed      int64
	observedTotal int64

	// emittedAny reports whether a byte event was already emitted in the
	// current stage; emitted is its byte count.
	emittedAny bool
	emitted    int64

	lastEmit time.Time
}

// newProgressEmitter creates an emitter for one Apply pipeline. A nil
// callback yields a fully inert emitter.
func newProgressEmitter(cb func(Progress), assetName string, minInterval time.Duration, now func() time.Time) *progressEmitter {
	if now == nil {
		now = time.Now
	}

	return &progressEmitter{
		cb:          cb,
		assetName:   assetName,
		minInterval: minInterval,
		now:         now,
	}
}

// transition reports the start of stage, resetting the byte tracking.
func (e *progressEmitter) transition(stage Stage) {
	if e.cb == nil {
		return
	}

	e.stage = stage
	e.observed = 0
	e.observedTotal = 0
	e.emittedAny = false
	e.emitted = 0

	e.cb(Progress{Stage: stage, AssetName: e.assetName})
}

// observe records a byte observation and emits an intermediate event when
// the throttle window allows one. The clock is read exactly once per
// observation, so an injected clock advances predictably.
func (e *progressEmitter) observe(current, total int64) {
	if e.cb == nil {
		return
	}

	e.observed = current
	e.observedTotal = total

	now := e.now()

	if !e.emittedAny {
		// The first observation of a stage carries the Total immediately,
		// so consumers learn determinacy before the window opens.
		e.emittedAny = true
		e.emitted = current
		e.lastEmit = now

		e.cb(Progress{Stage: e.stage, AssetName: e.assetName, Current: current, Total: total})

		return
	}

	if current == e.emitted || now.Sub(e.lastEmit) < e.minInterval {
		return
	}

	e.emitted = current
	e.lastEmit = now

	e.cb(Progress{Stage: e.stage, AssetName: e.assetName, Current: current, Total: total})
}

// finalize reports the successful completion of the current stage with the
// exact last observed byte counts. It is never called for a failed stage.
func (e *progressEmitter) finalize() {
	if e.cb == nil {
		return
	}

	e.cb(Progress{Stage: e.stage, AssetName: e.assetName, Current: e.observed, Total: e.observedTotal})
}

// completed reports the single end-of-pipeline event.
func (e *progressEmitter) completed() {
	if e.cb == nil {
		return
	}

	e.cb(Progress{Stage: StageCompleted, AssetName: e.assetName})
}

// progressWriter counts bytes copied through it and reports each chunk to
// onBytes, wrapping the destination of a streaming copy.
type progressWriter struct {
	w       io.Writer
	onBytes progressFunc
	total   int64

	n int64
}

// Write implements io.Writer.
func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)

	if n > 0 && p.onBytes != nil {
		p.n += int64(n)

		p.onBytes(p.n, p.total)
	}

	return n, err
}

// countingReader counts bytes read through it and reports each chunk to
// onBytes. It also enforces ctx between reads, so verifiers that ignore
// their context still abort streaming reads on cancellation.
type countingReader struct {
	ctx     context.Context
	r       io.Reader
	onBytes progressFunc
	total   int64

	n int64
}

// Read implements io.Reader.
func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	n, err := c.r.Read(p)

	if n > 0 {
		c.n += int64(n)

		if c.onBytes != nil {
			c.onBytes(c.n, c.total)
		}
	}

	return n, err
}
