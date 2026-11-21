package san_ops_pipeline

import (
	"io"
	"sync"
	"time"
)

// view is what a run reports to as it goes — the one seam between running and showing.
//
// Today's view is plainView: plain lines, for any Out (and Verbose). The live view redrawn in place
// for an interactive terminal (P2) implements the same interface; Run picks the view, the runner
// never knows which one it talks to. Every call comes through lockedView, one at a time, so a view
// needs no locking of its own even though output and tick arrive from a step's watcher goroutine.
type view interface {
	// runStart: the run folder exists and nothing has run yet. p is the whole pipeline, imports
	// included, so a view can lay out every job at once.
	runStart(p *Pipeline, res *Result)

	// jobStart: a job begins — a job with uses: too, before its own jobs start.
	jobStart(j jobRef)

	// stepStart: step n (1-based) of a job begins.
	stepStart(j jobRef, n int, s *Step)

	// output: one line the running step printed, without its line ending.
	output(j jobRef, line string)

	// tick: called every tickEvery() while a step runs; running is how long the step has run.
	tick(j jobRef, running time.Duration)

	// jobEnd: a job ended — or was skipped or not run, which come without a jobStart. tail holds
	// the last lines (at most tailLines) of the step that failed or was stopped; nil otherwise.
	jobEnd(j jobRef, r *JobResult, tail []string)

	// runEnd: the run is over and summary.md is written.
	runEnd(res *Result)

	// tickEvery is how often tick is called during a step (0: never).
	tickEvery() time.Duration
}

// tailLines is how many of a failing step's last lines are shown (default 13: 20 after a failure).
const tailLines = 20

// jobRef names a job at its place in the run.
type jobRef struct {
	Job   *Job
	Path  string // `checks`, or `lint/vet` for job vet of the file job lint imports
	Depth int    // 0 for the pipeline's own jobs, 1 inside an import, 2 inside an import's import
	Width int    // the longest job name of the job's own file, to align the columns
}

// newView picks the view for a run's options. P2 adds the live view here, chosen when Out is an
// interactive terminal; until then every run gets plain lines.
func newView(opts RunOptions) view {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	return &lockedView{v: &plainView{
		w:       out,
		verbose: opts.Verbose,
		still:   stillRunningEvery,
	}}
}

// lockedView passes every call on to its view one at a time.
type lockedView struct {
	mu sync.Mutex
	v  view
}

func (l *lockedView) runStart(p *Pipeline, res *Result) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.runStart(p, res)
}

func (l *lockedView) jobStart(j jobRef) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.jobStart(j)
}

func (l *lockedView) stepStart(j jobRef, n int, s *Step) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.stepStart(j, n, s)
}

func (l *lockedView) output(j jobRef, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.output(j, line)
}

func (l *lockedView) tick(j jobRef, running time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.tick(j, running)
}

func (l *lockedView) jobEnd(j jobRef, r *JobResult, tail []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.jobEnd(j, r, tail)
}

func (l *lockedView) runEnd(res *Result) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.v.runEnd(res)
}

func (l *lockedView) tickEvery() time.Duration {
	return l.v.tickEvery()
}
