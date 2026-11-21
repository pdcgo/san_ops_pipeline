package san_ops_pipeline

import (
	"fmt"
	"time"
)

// Status is how a job — or a whole run — ended.
type Status string

const (
	// Passed: every step exited 0 (for a job with uses:, every job of the imported file passed).
	Passed Status = "passed"

	// Failed: a step exited non-zero or could not start; the job's later steps did not run. A job
	// with uses: fails when any job of the imported file fails. A run fails when any job failed.
	Failed Status = "failed"

	// Skipped: the job did not run because a job it needs (directly or through others) did not pass.
	Skipped Status = "skipped"

	// Interrupted: the job (or the run) was stopped while it ran — ctx cancelled or Ctrl-C.
	Interrupted Status = "interrupted"

	// NotRun: the run was interrupted before this job's turn came.
	NotRun Status = "not run"
)

// Result is what a run did: its folder, how it ended, and each job. Run returns it for a run that
// happened — passed, failed or interrupted. A failed pipeline is a Result, not a Go error.
type Result struct {
	// Name is the pipeline's name.
	Name string

	// File is the pipeline file, relative to the root.
	File string

	// Dir is the run folder, `<root>/.san_ops_pipeline_cache/<stamp>-<name>/` (absolute): a log per
	// job and summary.md.
	Dir string

	// Status is Passed, Failed (a job failed) or Interrupted.
	Status Status

	// Started is when the run began (local time); Took is how long it ran.
	Started time.Time
	Took    time.Duration

	// Jobs are the pipeline's jobs in file order, each with how it ended.
	Jobs []JobResult
}

// Failed reports whether the run did not pass: a job failed, or the run was interrupted (Run then
// also returns ErrInterrupted). A nil Result — a run that could not start — counts as failed.
// (The shape — a Result with Failed, err only for runs that could not happen — is a build choice
// to confirm.)
func (r *Result) Failed() bool {
	return r == nil || r.Status != Passed
}

// JobResult is how one job ended.
type JobResult struct {
	// Name is the job's name in its own file.
	Name string

	// Status is how it ended.
	Status Status

	// Took is how long it ran (0 for a job that never started).
	Took time.Duration

	// Log is the job's log file relative to the run folder, with forward slashes (`checks.log`,
	// `lint/vet.log` for a job of an imported file). Empty for a job with uses: (its own jobs have
	// the logs) and for a job that never started.
	Log string

	// Step, Command and Exit tell, for a Failed or Interrupted job, which step stopped it (1-based),
	// its `run:` and its exit code (-1 when it could not start or was stopped).
	Step    int
	Command string
	Exit    int

	// Reason says why the step could not start (its `dir:` missing, say). Empty otherwise.
	Reason string

	// Needs is, for a Skipped job, the job it needs that did not pass.
	Needs string

	// Uses is the imported file, for a job with uses:. Jobs then holds that file's own jobs.
	Uses string
	Jobs []JobResult
}

// formatDuration writes a time the way the run's output and summary.md show it: `1m02s`, `13m58s`,
// `0m00s` (whole seconds; minutes may pass 60).
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}

	s := int64(d / time.Second)

	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}
