package san_ops_pipeline

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// liveLines is how many of the running step's last output lines the live view shows under it
// (confirmed build default 13: 5 under the running step).
const liveLines = 5

// liveRedrawEvery is how often the ticking timer redraws the live view — at most once a second.
const liveRedrawEvery = time.Second

// liveView draws a run in an interactive terminal, redrawn in place: a header with the pipeline,
// its run folder and the time so far; every job with its status — ✓ passed, ✗ failed or stopped,
// ⏭ skipped, ● running, · waiting — and its time; a ticking timer on the running job and the
// running step's last 5 lines under it. An imported file's jobs are shown indented under the job
// that imports it, from the start. When the run ends the view is replaced by the summary, the last
// 20 lines of each step that failed, and the run folder.
//
//	pipeline slice · 20261121-143005-slice             6m40s
//	  ✓ checks     go build ./... && go vet ./...      1m02s
//	  ● go-tests   go test ./backend/...               5m38s
//	      ok   …/order_service/order_v1        41.2s
//	      === RUN   TestRestockAccept
//	  · stories    waiting
//	  · e2e        waiting (needs go-tests)
//
// It redraws when a job or step starts or ends, and on the runner's tick (liveRedrawEvery) while a
// step runs, so the timer moves once a second; output lines alone never redraw. It starts no
// goroutine of its own: the tick comes from the step's watcher goroutine, which ends with the step.
//
// Every line of a redraw is cut to the terminal's width minus one column, so it never wraps, and a
// frame taller than the terminal keeps the running job in sight ("… n more above/below"); the
// final summary is printed once and left to scroll like any output, so it is not cut.
type liveView struct {
	t     *terminal
	now   func() time.Time
	every time.Duration // the shortest gap between two timer redraws of a step

	name    string
	stamp   string // the run folder's own name, `20261121-143005-slice`
	root    string
	started time.Time

	jobs   []*liveJob // in the file's order, an import's jobs right after the job that imports it
	byPath map[string]*liveJob

	drawn  int // lines of the live frame now on the screen, above the cursor
	closed bool
}

// liveJob is one job as the live view knows it.
type liveJob struct {
	name  string
	path  string   // jobRef.Path: `checks`, `lint/vet`
	depth int      // import level
	width int      // the longest job name of its file, to align the columns
	needs []string // paths of the jobs it needs
	uses  string
	steps []*Step

	running  bool
	started  time.Time
	step     int           // the running step, 1-based
	cmd      string        // its command's first line
	lines    []string      // its last liveLines output lines
	lastTick time.Duration // the step's running time at the last timer redraw

	end  *JobResult // how it ended; nil while waiting or running
	tail []string   // the failing step's last lines
}

func newLiveView(t *terminal) *liveView {
	return &liveView{
		t:      t,
		now:    time.Now,
		every:  liveRedrawEvery,
		byPath: make(map[string]*liveJob),
	}
}

func (v *liveView) runStart(p *Pipeline, res *Result) {
	v.name = res.Name
	v.stamp = filepath.Base(res.Dir)
	v.root = p.root
	v.started = res.Started
	v.addJobs(p, "", 0)
	v.redraw()
}

// addJobs lists a file's jobs, and right after a job with uses: the imported file's own.
func (v *liveView) addJobs(p *Pipeline, prefix string, depth int) {
	width := 0
	for _, j := range p.Jobs {
		width = max(width, len(j.Name))
	}

	for _, j := range p.Jobs {
		lj := &liveJob{
			name:  j.Name,
			path:  prefix + j.Name,
			depth: depth,
			width: width,
			uses:  j.Uses,
			steps: j.Steps,
		}

		for _, need := range j.Needs {
			lj.needs = append(lj.needs, prefix+need)
		}

		v.jobs = append(v.jobs, lj)
		v.byPath[lj.path] = lj

		if j.Import != nil {
			v.addJobs(j.Import, prefix+j.Name+"/", depth+1)
		}
	}
}

func (v *liveView) jobStart(j jobRef) {
	lj := v.byPath[j.Path]
	if lj == nil {
		return
	}

	lj.running = true
	lj.started = v.now()
	v.redraw()
}

func (v *liveView) stepStart(j jobRef, n int, s *Step) {
	lj := v.byPath[j.Path]
	if lj == nil {
		return
	}

	lj.step = n
	lj.cmd = firstLine(s.Run)
	lj.lines = nil
	lj.lastTick = 0
	v.redraw()
}

// output keeps the line for the next redraw; it does not redraw by itself.
func (v *liveView) output(j jobRef, line string) {
	lj := v.byPath[j.Path]
	if lj == nil {
		return
	}

	lj.lines = append(lj.lines, line)
	if len(lj.lines) > liveLines {
		lj.lines = lj.lines[len(lj.lines)-liveLines:]
	}
}

// tick redraws at most once every v.every of the step's running time.
func (v *liveView) tick(j jobRef, running time.Duration) {
	lj := v.byPath[j.Path]
	if lj == nil || running-lj.lastTick < v.every {
		return
	}

	lj.lastTick = running
	v.redraw()
}

func (v *liveView) tickEvery() time.Duration {
	return v.every
}

func (v *liveView) jobEnd(j jobRef, r *JobResult, tail []string) {
	lj := v.byPath[j.Path]
	if lj == nil {
		return
	}

	end := *r
	lj.end = &end
	lj.tail = tail
	lj.running = false
	lj.lines = nil
	v.redraw()
}

// runEnd replaces the live frame with the final summary, printed once.
func (v *liveView) runEnd(res *Result) {
	var b strings.Builder

	v.back(&b)
	b.WriteString("\x1b[J")

	for _, line := range v.final(res) {
		b.WriteString(line)
		b.WriteString("\n")
	}

	v.drawn = 0
	v.write(b.String())
}

// close puts the terminal back (the Windows console mode); Run calls it as it returns.
func (v *liveView) close() {
	if v.closed {
		return
	}

	v.closed = true
	v.t.restore()
}

// redraw draws the frame again in place of the one on the screen: back to its first line, each
// line erased and written, then whatever is left below erased — one write, so it does not flicker.
func (v *liveView) redraw() {
	width, height := v.t.size()
	lines := v.frame(v.now(), width, height)

	var b strings.Builder

	v.back(&b)

	for _, line := range lines {
		b.WriteString("\x1b[2K")
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\x1b[J")

	v.drawn = len(lines)
	v.write(b.String())
}

// back moves the cursor to the first line of the frame on the screen.
func (v *liveView) back(b *strings.Builder) {
	if v.drawn > 0 {
		fmt.Fprintf(b, "\r\x1b[%dA", v.drawn)
	}
}

func (v *liveView) write(s string) {
	// A broken Out does not stop a run: the logs and summary.md still hold everything.
	_, _ = v.t.w.Write([]byte(s))
}

// liveRow is one line of a frame: the text left of the time column and the time.
type liveRow struct {
	left   string
	time   string
	output bool // a line of the running step's output, drawn as it is (cut only)
}

// frame renders the live view at now for a terminal of width × height (height 0: unknown) — the
// pure part of a redraw, tested with golden strings.
func (v *liveView) frame(now time.Time, width, height int) []string {
	limit := width - 1

	rows := []liveRow{{
		left: "pipeline " + v.name + " · " + v.stamp,
		time: formatDuration(now.Sub(v.started)),
	}}

	focusFrom, focusTo := -1, -1

	for _, j := range v.jobs {
		mark, desc := v.describe(j)
		rows = append(rows, liveRow{
			left: "  " + strings.Repeat("  ", j.depth) + mark + " " + pad(j.name, j.width) + "   " + desc,
			time: v.jobTime(j, now),
		})

		if j.running && j.uses == "" {
			focusFrom = len(rows) - 1

			for _, line := range j.lines {
				rows = append(rows, liveRow{
					left:   "  " + strings.Repeat("  ", j.depth) + "    " + screenText(line),
					output: true,
				})
			}

			focusTo = len(rows) - 1
		}
	}

	timeW, leftW := 0, 0

	for _, r := range rows {
		if r.output {
			continue
		}

		timeW = max(timeW, len(r.time))
		leftW = max(leftW, cells(r.left))
	}

	leftW = min(leftW, limit-3-timeW)

	lines := make([]string, 0, len(rows))

	for _, r := range rows {
		s := r.left

		switch {
		case r.output || r.time == "" || leftW < 1:
			// drawn as it is, cut to the width below
		default:
			s = padCells(cutCells(r.left, leftW), leftW) + "   " + strings.Repeat(" ", timeW-len(r.time)) + r.time
		}

		lines = append(lines, cutCells(s, limit))
	}

	return fitHeight(lines, height, limit, focusFrom, focusTo)
}

// describe is a job's symbol and what its line says.
func (v *liveView) describe(j *liveJob) (mark, desc string) {
	switch {
	case j.end != nil:
		r := j.end

		switch r.Status {
		case Passed:
			desc = passedDesc(j)
		case Failed:
			desc = failedDesc(r)
		case Interrupted:
			desc = "interrupted"
			if r.Uses == "" {
				desc = firstLine(r.Command) + " (interrupted)"
			}
		case Skipped:
			desc = "skipped (needs " + r.Needs + ")"
		default:
			desc = string(r.Status)
		}

		return statusMark(r.Status), desc
	case j.running:
		cmd, step := j.cmd, j.step
		if step == 0 && len(j.steps) > 0 {
			cmd, step = firstLine(j.steps[0].Run), 1 // the job has begun, its first step not yet
		}

		switch {
		case j.uses != "":
			desc = "uses " + j.uses
		case len(j.steps) > 1:
			desc = fmt.Sprintf("%d/%d · %s", step, len(j.steps), cmd)
		default:
			desc = cmd
		}

		return "●", desc
	}

	var open []string

	for _, need := range j.needs {
		n := v.byPath[need]
		if n != nil && n.end == nil {
			open = append(open, n.name)
		}
	}

	if len(open) > 0 {
		return "·", "waiting (needs " + strings.Join(open, ", ") + ")"
	}

	return "·", "waiting"
}

// passedDesc: a passed job's line shows its command — or how many steps it had, or what it imports.
func passedDesc(j *liveJob) string {
	switch {
	case j.uses != "":
		return "uses " + j.uses
	case len(j.steps) == 1:
		return firstLine(j.steps[0].Run)
	default:
		return fmt.Sprintf("%d steps", len(j.steps))
	}
}

// failedDesc: what stopped a failed job — the step's command and exit code, why it could not
// start, or, for a job with uses:, which of its own jobs failed.
func failedDesc(r *JobResult) string {
	switch {
	case r.Uses != "":
		return "failed: " + strings.Join(failedJobs(r.Jobs), ", ")
	case r.Reason != "":
		return "could not start: " + r.Reason
	default:
		return fmt.Sprintf("%s (exit %d)", firstLine(r.Command), r.Exit)
	}
}

// jobTime is the time column of a job's line: ticking while it runs, its time once it ran.
func (v *liveView) jobTime(j *liveJob, now time.Time) string {
	switch {
	case j.end != nil:
		if j.end.Status == Skipped || j.end.Status == NotRun {
			return ""
		}

		return formatDuration(j.end.Took)
	case j.running:
		return formatDuration(now.Sub(j.started))
	default:
		return ""
	}
}

// fitHeight keeps a frame within height-1 lines (a frame taller than the terminal cannot be drawn
// again in place: the cursor cannot go back up past the screen's top). The header stays; of the
// rest it keeps the window that shows lines focusFrom..focusTo (the running job and its output),
// with a `… n more above` / `… n more below` line for what is left out.
func fitHeight(lines []string, height, limit, focusFrom, focusTo int) []string {
	if height <= 0 {
		return lines
	}

	room := max(height-1, 4)
	if len(lines) <= room {
		return lines
	}

	body := lines[1:]
	win := room - 3 // the header and the two `… more` lines take the rest

	if focusFrom < 0 {
		focusFrom, focusTo = len(lines)-1, len(lines)-1
	}

	// focus in body's indexes
	from, to := focusFrom-1, focusTo-1

	start := max(0, to-win+1)
	start = min(start, from)
	start = min(start, len(body)-win)
	start = max(start, 0)

	out := make([]string, 0, room)
	out = append(out, lines[0])

	if start > 0 {
		out = append(out, cutCells(fmt.Sprintf("  … %d more above", start), limit))
	}

	out = append(out, body[start:start+win]...)

	below := len(body) - start - win
	if below > 0 {
		out = append(out, cutCells(fmt.Sprintf("  … %d more below", below), limit))
	}

	return out
}

// final renders the summary that replaces the live view when the run ends: the result, each job
// (an imported file's jobs indented under the job that imports it), the last lines of every step
// that failed or was stopped, and the run folder.
//
//	pipeline slice · FAILED in 13m58s
//	  ✓ checks     1m02s
//	  ✗ go-tests   5m38s   go test ./backend/... (exit 1)
//	  ✓ stories    7m15s
//	  ⏭ e2e        skipped (needs go-tests)
//
//	go-tests · last lines of go-tests.log
//	  --- FAIL: TestRestockAccept (0.42s)
//	logs: .san_ops_pipeline_cache/20261121-143005-slice/
func (v *liveView) final(res *Result) []string {
	verb := "PASSED in"

	switch res.Status {
	case Failed:
		verb = "FAILED in"
	case Interrupted:
		verb = "INTERRUPTED after"
	}

	out := []string{fmt.Sprintf("pipeline %s · %s %s", res.Name, verb, formatDuration(res.Took))}

	type row struct{ lead, time, detail string }

	var rows []row

	var walk func(jobs []JobResult, depth int)

	walk = func(jobs []JobResult, depth int) {
		width := 0
		for _, j := range jobs {
			width = max(width, len(j.Name))
		}

		for i := range jobs {
			j := &jobs[i]
			r := row{lead: "  " + strings.Repeat("  ", depth) + statusMark(j.Status) + " " + pad(j.Name, width) + "   "}

			switch j.Status {
			case Passed:
				r.time = formatDuration(j.Took)
			case Failed:
				r.time = formatDuration(j.Took)
				r.detail = failedDesc(j)
			case Interrupted:
				r.time = formatDuration(j.Took)
				r.detail = "interrupted"
				if j.Uses == "" {
					r.detail = firstLine(j.Command) + " (interrupted)"
				}
			case Skipped:
				r.detail = "skipped (needs " + j.Needs + ")"
			default:
				r.detail = string(j.Status)
			}

			rows = append(rows, r)
			walk(j.Jobs, depth+1)
		}
	}

	walk(res.Jobs, 0)

	timeW := 0
	for _, r := range rows {
		timeW = max(timeW, len(r.time))
	}

	for _, r := range rows {
		s := r.lead

		if r.time != "" {
			s += strings.Repeat(" ", timeW-len(r.time)) + r.time
			if r.detail != "" {
				s += "   "
			}
		}

		out = append(out, s+r.detail)
	}

	for _, j := range v.jobs {
		if j.end == nil || len(j.tail) == 0 || (j.end.Status != Failed && j.end.Status != Interrupted) {
			continue
		}

		out = append(out, "", j.path+" · last lines of "+j.end.Log)

		for _, line := range j.tail {
			out = append(out, "  "+screenText(line))
		}
	}

	return append(out, "logs: "+displayPath(v.root, res.Dir)+"/")
}
