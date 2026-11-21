package san_ops_pipeline

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// stepOutcome is how one step ended.
type stepOutcome struct {
	exit        int
	reason      string // why it could not start; empty when it ran
	interrupted bool
	tail        []string
}

// step runs one step: a header into the job's log, `bash -c` with the log as its stdout and
// stderr, the log read back for new lines while it runs, then a closing line with the exit code and
// time.
//
// Build choices (to confirm): the step gets no input (stdin is the null device), and when its bash
// exits whatever it left running in the background is stopped (tree.end) — no step outlives its turn.
//
// The step writes straight into the log file — there is no pipe between it and this program. A
// pipe stays open as long as ANY process holds its write end, so a background child the step
// left behind would keep a reader waiting for ever; a file has no such end. The lines the view
// shows are read back from the file every pollEvery.
func (r *runner) step(ref jobRef, log, back *os.File, s *Step, n, total int, env []string) stepOutcome {
	dir := r.root
	shown := ""

	if s.Dir != "" {
		dir = filepath.FromSlash(s.Dir)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(r.root, dir)
		}

		shown = " · dir " + s.Dir
	}

	started := time.Now()
	r.logf(log, "── step %d/%d · started %s%s\n", n, total, started.Format("2006-01-02 15:04:05"), shown)

	for _, line := range strings.Split(strings.TrimRight(s.Run, "\n"), "\n") {
		r.logf(log, "$ %s\n", strings.TrimRight(line, "\r"))
	}

	r.view.stepStart(ref, n, s)

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		reason := fmt.Sprintf("dir %s is not a folder under the root", s.Dir)
		r.logf(log, "── step %d/%d · could not start: %s\n", n, total, reason)

		return stepOutcome{exit: -1, reason: reason}
	}

	stat, err := log.Stat()
	if err != nil {
		r.logf(log, "── step %d/%d · could not start: %v\n", n, total, err)

		return stepOutcome{exit: -1, reason: err.Error()}
	}

	offset := stat.Size()

	cmd := bashCommand(r.bash, s.Run)
	cmd.Dir = dir
	cmd.Env = withEnv(env, s.Env)
	cmd.Stdout = log
	cmd.Stderr = log

	tree, err := startTree(cmd)
	if err != nil {
		reason := "bash: " + err.Error()
		r.logf(log, "── step %d/%d · could not start: %s\n", n, total, reason)

		return stepOutcome{exit: -1, reason: reason}
	}

	lines := &lineReader{file: back, off: offset}
	emit := func(line string) {
		r.view.output(ref, line)
	}

	var (
		killed atomic.Bool
		wg     sync.WaitGroup
	)

	done := make(chan struct{})

	wg.Add(1)

	go func() {
		defer wg.Done()

		poll := time.NewTicker(r.pollEvery)
		defer poll.Stop()

		lastTick := started

		for {
			select {
			case <-done:
				return
			case <-r.ctx.Done():
				killed.Store(true)
				tree.kill()

				return
			case now := <-poll.C:
				lines.read(emit)

				if r.tickEvery > 0 && now.Sub(lastTick) >= r.tickEvery {
					lastTick = now
					r.view.tick(ref, now.Sub(started))
				}
			}
		}
	}()

	_ = cmd.Wait()

	close(done)
	wg.Wait()
	tree.end()

	lines.read(emit)
	lines.flush(emit)

	took := time.Since(started)
	out := stepOutcome{exit: cmd.ProcessState.ExitCode(), tail: lines.tail()}

	gap := ""
	if !lines.ended() {
		gap = "\n"
	}

	if killed.Load() {
		out.interrupted = true
		out.exit = -1
		r.logf(log, "%s── step %d/%d · interrupted after %s — stopped with every process it started\n", gap, n, total, formatDuration(took))

		return out
	}

	r.logf(log, "%s── step %d/%d · exit %d · %s\n", gap, n, total, out.exit, formatDuration(took))

	return out
}

// Limits of what lineReader keeps: a line longer than maxLine is cut there and goes on as a new
// line (a progress bar that never prints a newline still shows); tail lines are cut at maxTailLine.
const (
	maxLine     = 64 << 10
	maxTailLine = 2000
)

// lineReader reads a step's output back from its job log, from the offset the step began at,
// splitting it into lines and keeping the last tailLines of them.
type lineReader struct {
	file *os.File
	off  int64
	buf  []byte
	part []byte // a line read so far without its newline
	last []string
	any  bool // the step printed something
	nl   bool // ... and its last byte was a newline
}

// read takes everything written since the last read.
func (l *lineReader) read(emit func(string)) {
	if l.buf == nil {
		l.buf = make([]byte, 32<<10)
	}

	for {
		n, err := l.file.ReadAt(l.buf, l.off)
		if n > 0 {
			l.off += int64(n)
			l.feed(l.buf[:n], emit)
		}

		if err != nil || n < len(l.buf) {
			return
		}
	}
}

func (l *lineReader) feed(b []byte, emit func(string)) {
	l.any = true
	l.nl = b[len(b)-1] == '\n'

	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			l.part = append(l.part, b...)
			if len(l.part) >= maxLine {
				l.line(emit)
			}

			return
		}

		l.part = append(l.part, b[:i]...)
		l.line(emit)
		b = b[i+1:]
	}
}

func (l *lineReader) line(emit func(string)) {
	s := strings.TrimRight(string(l.part), "\r")
	l.part = l.part[:0]

	t := s
	if len(t) > maxTailLine {
		t = strings.ToValidUTF8(t[:maxTailLine], "") + " …"
	}

	l.last = append(l.last, t)
	if len(l.last) > tailLines {
		l.last = l.last[len(l.last)-tailLines:]
	}

	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}

	emit(s)
}

// flush passes on a last line the step printed without a newline.
func (l *lineReader) flush(emit func(string)) {
	if len(l.part) > 0 {
		l.line(emit)
	}
}

// ended reports whether the log ends on a line of its own (nothing printed, or a final newline),
// so the closing line can follow without a blank line before it.
func (l *lineReader) ended() bool {
	return !l.any || l.nl
}

// tail is a copy of the last lines read.
func (l *lineReader) tail() []string {
	return append([]string(nil), l.last...)
}
