package san_ops_pipeline

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// stillRunningEvery is how often plain lines say a long step is still running.
const stillRunningEvery = 5 * time.Minute

// plainView prints a run as plain lines: one when a job starts, one when it ends (time, exit, log),
// the failing step's last lines, a `still running` line every 5 minutes of a long step, and a final
// line with the result and the run folder. An imported file's jobs are indented under the job that
// imports it. With verbose, every output line is printed too, with `[job]` in front, and the last
// lines after a failure are left out (they were just printed).
//
//	→ go-tests  go test ./backend/...
//	✗ go-tests  5m38s  exit 1 · go-tests.log
//	    --- FAIL: TestRestockAccept (0.42s)
//	⏭ e2e       skipped (needs go-tests)
//	FAILED in 13m58s · logs: .san_ops_pipeline_cache/20261121-143005-slice/
type plainView struct {
	w       io.Writer
	verbose bool
	still   time.Duration
	root    string // the pipeline's root, set at runStart
}

func (v *plainView) printf(format string, args ...any) {
	// A broken Out does not stop a run: the logs and summary.md still hold everything.
	_, _ = fmt.Fprintf(v.w, format, args...)
}

func (v *plainView) runStart(p *Pipeline, res *Result) {
	v.root = p.root
	v.printf("pipeline %s · %s\n", res.Name, filepath.Base(res.Dir))
}

// lead is a job line's start: the indent of its import level, the symbol, the padded name.
func lead(j jobRef, mark string) string {
	return strings.Repeat("  ", j.Depth) + mark + " " + pad(j.Job.Name, j.Width)
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}

	return s + strings.Repeat(" ", width-len(s))
}

func (v *plainView) jobStart(j jobRef) {
	what := ""

	switch {
	case j.Job.Import != nil:
		what = "uses " + j.Job.Uses
	case len(j.Job.Steps) > 0:
		what = firstLine(j.Job.Steps[0].Run)
	}

	v.printf("%s  %s\n", lead(j, "→"), what)
}

func (v *plainView) stepStart(j jobRef, n int, s *Step) {
	if v.verbose {
		v.printf("[%s] $ %s\n", j.Path, firstLine(s.Run))
	}
}

func (v *plainView) output(j jobRef, line string) {
	if v.verbose {
		v.printf("[%s] %s\n", j.Path, line)
	}
}

func (v *plainView) tick(j jobRef, running time.Duration) {
	v.printf("%s  still running %dm\n", lead(j, "●"), int(running/time.Minute))
}

func (v *plainView) tickEvery() time.Duration {
	return v.still
}

func (v *plainView) jobEnd(j jobRef, r *JobResult, tail []string) {
	line := lead(j, statusMark(r.Status)) + "  "
	took := formatDuration(r.Took)

	switch r.Status {
	case Passed:
		line += took
	case Failed:
		switch {
		case r.Uses != "":
			line += took + "  failed: " + strings.Join(failedJobs(r.Jobs), ", ")
		case r.Reason != "":
			line += took + "  could not start: " + r.Reason
		default:
			line += fmt.Sprintf("%s  exit %d", took, r.Exit)
		}
	case Interrupted:
		line += took + "  interrupted"
	case Skipped:
		line += "skipped (needs " + r.Needs + ")"
	default:
		line += string(r.Status)
	}

	if r.Log != "" && r.Status != NotRun && r.Status != Skipped {
		line += " · " + r.Log
	}

	v.printf("%s\n", line)

	if v.verbose {
		return
	}

	indent := strings.Repeat("  ", j.Depth) + "    "
	for _, t := range tail {
		v.printf("%s%s\n", indent, t)
	}
}

// failedJobs names the jobs of an imported file that failed or were stopped.
func failedJobs(jobs []JobResult) []string {
	var out []string

	for _, j := range jobs {
		if j.Status == Failed || j.Status == Interrupted {
			out = append(out, j.Name)
		}
	}

	return out
}

// close: plain lines change nothing that needs putting back.
func (v *plainView) close() {}

func (v *plainView) runEnd(res *Result) {
	verb := "PASSED in"

	switch res.Status {
	case Failed:
		verb = "FAILED in"
	case Interrupted:
		verb = "INTERRUPTED after"
	}

	v.printf("%s %s · logs: %s/\n", verb, formatDuration(res.Took), displayPath(v.root, res.Dir))
}
