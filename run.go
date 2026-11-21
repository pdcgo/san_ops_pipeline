package san_ops_pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"
)

// RunOptions are the choices of one run.
type RunOptions struct {
	// Out receives the run's progress. When it is an interactive terminal (an *os.File such as
	// os.Stdout on a console), it shows a live view redrawn in place: every job with its status and
	// time, a ticking timer on the running one and the running step's last 5 lines under it; at the
	// end the summary, the failing step's last 20 lines and the run folder. Anything else (a
	// buffer, a file, a pipe) gets plain lines: one when a job starts, one when it ends, the
	// failing step's last 20 lines, a `still running` line every 5 minutes of a long step, and a
	// final line with the result and the run folder. Nil prints nothing; the job logs and
	// summary.md are written either way.
	Out io.Writer

	// Verbose prints every line the steps print, each with `[job]` in front (`[lint/vet]` for a job
	// of an imported file), as plain lines — in a terminal too, instead of the live view.
	Verbose bool
}

// errCtrlC is the cause of a run Ctrl-C stopped.
var errCtrlC = errors.New("Ctrl-C")

// runConfig holds what the tests change; a zero field means the real thing.
type runConfig struct {
	findBash  func() (string, error)
	tickEvery time.Duration // replaces the view's own (5 minutes for plain lines, 1 second live)
	pollEvery time.Duration // how often a running step's log is read for new lines
	term      *termSys      // replaces the real terminal (the live view's choice and console mode)
}

const defaultPollEvery = 200 * time.Millisecond

// Run runs the pipeline: one job at a time, in `needs` order and the file's order where `needs`
// leaves it open; a job's steps one after another, each with `bash -c`.
//
// A step that exits non-zero fails its job: the job's later steps do not run, every job that needs
// it — directly or through others — is skipped, and every other job still runs. A job with uses:
// runs the imported file's jobs by the same rule and fails when any of them fails. The run then
// ends Failed: that is a Result (res.Failed()), not an error.
//
// Each step runs in the caller's environment plus the env: values — the pipeline's, the job's, the
// step's, the inner one winning (for a job with uses:, the importing file's values come first and
// the imported file's own go on top). It starts in the root, or in its dir: read from the root.
//
// The run writes `<root>/.san_ops_pipeline_cache/<stamp>-<name>/`: a `<job>.log` per job (an
// imported file's job logs in a sub-folder named after the importing job) and summary.md.
//
// Its progress goes to opts.Out: a live view redrawn in place when Out is an interactive terminal
// (on Windows the console's escape-code mode is turned on for the run and put back after it),
// plain lines for anything else, every line with Verbose — see RunOptions.
//
// Cancelling ctx — or pressing Ctrl-C, which Run catches while it runs — stops the running step
// together with every process it started, starts nothing more, and returns the Result with
// ErrInterrupted. When a step's bash exits, anything it left running in the background is stopped
// too, so no step outlives its turn.
//
// err is for a run that could not happen or was cut short: no bash (ErrNoBash, before anything
// starts), a run folder or log that cannot be written, or an interruption.
func (p *Pipeline) Run(ctx context.Context, opts RunOptions) (*Result, error) {
	return p.run(ctx, opts, runConfig{})
}

func (p *Pipeline) run(ctx context.Context, opts RunOptions, cfg runConfig) (*Result, error) {
	find := cfg.findBash
	if find == nil {
		find = findBash
	}

	bash, err := find()
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	stopWatching := watchCtrlC(cancel)
	defer stopWatching()

	started := time.Now()

	dir, err := makeRunDir(p.root, stamp(started), p.Name)
	if err != nil {
		return nil, err
	}

	res := &Result{Name: p.Name, File: p.File, Dir: dir, Started: started}

	v := newView(opts, cfg)
	defer v.close()

	r := &runner{
		ctx:       runCtx,
		bash:      bash,
		root:      p.root,
		view:      v,
		tickEvery: v.tickEvery(),
		pollEvery: defaultPollEvery,
	}

	if cfg.tickEvery > 0 {
		r.tickEvery = cfg.tickEvery
	}

	if cfg.pollEvery > 0 {
		r.pollEvery = cfg.pollEvery
	}

	v.runStart(p, res)

	res.Jobs = r.pipeline(p, os.Environ(), dir, "", 0)
	res.Took = time.Since(started)

	switch {
	case r.interrupted:
		res.Status = Interrupted
	case anyFailed(res.Jobs):
		res.Status = Failed
	default:
		res.Status = Passed
	}

	err = writeSummary(res, r.stoppedIn)
	if err != nil && r.err == nil {
		r.err = err
	}

	v.runEnd(res)

	if r.err != nil {
		return res, r.err
	}

	if res.Status == Interrupted {
		return res, fmt.Errorf("%w: %w", ErrInterrupted, context.Cause(runCtx))
	}

	return res, nil
}

// watchCtrlC turns Ctrl-C into a cancelled run while Run runs. Build choice (to confirm): the library
// catches Ctrl-C itself, because a step runs outside the terminal's foreground process group (unix)
// or console group (Windows) and would otherwise outlive a caller killed by its Ctrl-C. Its goroutine has ended, and Ctrl-C
// is back to what it was, when the returned stop function returns.
func watchCtrlC(cancel context.CancelCauseFunc) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	quit := make(chan struct{})

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		select {
		case <-sig:
			cancel(errCtrlC)
		case <-quit:
		}
	}()

	return func() {
		signal.Stop(sig)
		close(quit)
		wg.Wait()
	}
}

func anyFailed(jobs []JobResult) bool {
	for _, j := range jobs {
		if j.Status == Failed {
			return true
		}
	}

	return false
}

// runner runs one Run's jobs.
type runner struct {
	ctx       context.Context
	bash      string
	root      string
	view      view
	tickEvery time.Duration
	pollEvery time.Duration

	interrupted bool   // a job was stopped, or not started, because the run was cancelled
	stoppedIn   string // the job that was running when it was
	err         error  // a log or folder that could not be written; the run stops
}

// stopped reports whether nothing more may start.
func (r *runner) stopped() bool {
	return r.ctx.Err() != nil || r.err != nil
}

// pipeline runs one file's jobs — the run's own file, or an imported one. env is the environment
// so far, logDir where this file's job logs go, prefix the job path of the importing job.
func (r *runner) pipeline(p *Pipeline, env []string, logDir, prefix string, depth int) []JobResult {
	env = withEnv(env, p.Env)

	width := 0
	index := make(map[string]int, len(p.Jobs))

	for i, j := range p.Jobs {
		index[j.Name] = i
		width = max(width, len(j.Name))
	}

	results := make([]JobResult, len(p.Jobs))
	done := make([]bool, len(p.Jobs))

	for range p.Jobs {
		i := nextJob(p.Jobs, done, index)
		done[i] = true

		j := p.Jobs[i]
		ref := jobRef{Job: j, Path: prefix + j.Name, Depth: depth, Width: width}
		res := &results[i]
		res.Name = j.Name
		res.Uses = j.Uses

		if r.stopped() {
			res.Status = NotRun
			r.interrupted = r.interrupted || r.ctx.Err() != nil
			r.view.jobEnd(ref, res, nil)

			continue
		}

		blocked := ""

		for _, need := range j.Needs {
			if results[index[need]].Status != Passed {
				blocked = need

				break
			}
		}

		if blocked != "" {
			res.Status = Skipped
			res.Needs = blocked
			r.view.jobEnd(ref, res, nil)

			continue
		}

		if j.Import != nil {
			r.importJob(ref, res, env, logDir, prefix, depth)
		} else {
			r.stepsJob(ref, res, env, logDir, prefix)
		}
	}

	return results
}

// nextJob picks the job to run next: the first in file order whose needs have all finished. Load
// refused every `needs` loop and every missing job, so there always is one.
func nextJob(jobs []*Job, done []bool, index map[string]int) int {
	first := -1

	for i, j := range jobs {
		if done[i] {
			continue
		}

		if first < 0 {
			first = i
		}

		ready := true

		for _, need := range j.Needs {
			if !done[index[need]] {
				ready = false

				break
			}
		}

		if ready {
			return i
		}
	}

	return first
}

// importJob runs a job with uses: — the imported file's jobs, as this one job, their logs in a
// sub-folder named after it.
func (r *runner) importJob(ref jobRef, res *JobResult, env []string, logDir, prefix string, depth int) {
	j := ref.Job
	env = withEnv(env, j.Env)

	sub := filepath.Join(logDir, j.Name)

	err := os.Mkdir(sub, 0o755)
	if err != nil {
		r.err = fmt.Errorf("san_ops_pipeline: create the log folder of job %s: %w", ref.Path, err)
		res.Status = NotRun
		r.view.jobEnd(ref, res, nil)

		return
	}

	started := time.Now()
	r.view.jobStart(ref)

	res.Jobs = r.pipeline(j.Import, env, sub, prefix+j.Name+"/", depth+1)
	res.Took = time.Since(started)
	res.Status = Passed

	for _, inner := range res.Jobs {
		switch inner.Status {
		case Interrupted, NotRun:
			res.Status = Interrupted
		case Failed:
			if res.Status == Passed {
				res.Status = Failed
			}
		}
	}

	if res.Status == Interrupted && r.stoppedIn == "" {
		r.stoppedIn = ref.Path
	}

	r.view.jobEnd(ref, res, nil)
}

// stepsJob runs a job's steps one after another into its log; the first failing step ends it.
func (r *runner) stepsJob(ref jobRef, res *JobResult, env []string, logDir, prefix string) {
	j := ref.Job
	env = withEnv(env, j.Env)

	logPath := filepath.Join(logDir, j.Name+".log")

	// No O_APPEND: on Windows Go then opens the file for FILE_APPEND_DATA only, and MSYS bash
	// cannot write its stdout into such a handle (every `echo` exits 1). The step inherits this
	// handle, so it shares the file position: the header, the step's output and the closing line
	// land one after another.
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		r.err = fmt.Errorf("san_ops_pipeline: create the log of job %s: %w", ref.Path, err)
		res.Status = NotRun
		r.view.jobEnd(ref, res, nil)

		return
	}

	defer log.Close()

	back, err := os.Open(logPath)
	if err != nil {
		r.err = fmt.Errorf("san_ops_pipeline: read the log of job %s: %w", ref.Path, err)
		res.Status = NotRun
		r.view.jobEnd(ref, res, nil)

		return
	}

	defer back.Close()

	res.Log = prefix + j.Name + ".log"
	started := time.Now()
	r.view.jobStart(ref)

	status := Passed

	var tail []string

	for i, s := range j.Steps {
		n := i + 1

		if status != Passed {
			r.logf(log, "── step %d/%d · not run (an earlier step %s)\n", n, len(j.Steps), status)

			continue
		}

		if r.stopped() {
			status = Interrupted
			res.Step = n
			res.Command = s.Run
			res.Exit = -1
			r.logf(log, "── step %d/%d · not run (the run was interrupted)\n", n, len(j.Steps))

			continue
		}

		out := r.step(ref, log, back, s, n, len(j.Steps), env)

		switch {
		case out.interrupted:
			status = Interrupted
			res.Exit = -1
		case out.reason != "":
			status = Failed
			res.Reason = out.reason
			res.Exit = -1
		case out.exit != 0:
			status = Failed
			res.Exit = out.exit
		}

		if status != Passed {
			res.Step = n
			res.Command = s.Run
			tail = out.tail
		}
	}

	res.Status = status
	res.Took = time.Since(started)

	if status == Interrupted {
		r.interrupted = r.interrupted || r.ctx.Err() != nil
		if r.stoppedIn == "" {
			r.stoppedIn = ref.Path
		}
	}

	r.view.jobEnd(ref, res, tail)
}

// logf writes one of the runner's own lines into a job log. The first write that fails stops the
// run (r.err): a log that cannot be written is a run that cannot be trusted.
func (r *runner) logf(log *os.File, format string, args ...any) {
	_, err := fmt.Fprintf(log, format, args...)
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("san_ops_pipeline: write %s: %w", log.Name(), err)
	}
}
