package san_ops_pipeline

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoBash is returned by Run, before anything starts, when no usable bash is found. On Windows
// that is Git for Windows' bash (see the README's *Which bash*); the WSL launchers
// C:\Windows\System32\bash.exe and …\WindowsApps\bash.exe are never used, as they run Linux.
var ErrNoBash = errors.New("san_ops_pipeline: no bash found")

// ErrInterrupted is returned by Run when the run was stopped before its end — the caller's ctx was
// cancelled or Ctrl-C was pressed. Run returns the Result too: the stopped job is Interrupted, the
// jobs after it NotRun, and summary.md says so. The error also wraps the context's cause, so
// errors.Is(err, context.Canceled) and errors.Is(err, context.DeadlineExceeded) work.
var ErrInterrupted = errors.New("san_ops_pipeline: run interrupted")

// Problem is one thing Load refused in a pipeline file.
type Problem struct {
	// File is the pipeline file, relative to the root with forward slashes.
	File string

	// Line is the 1-based line in File, or 0 when the problem is the file as a whole (it cannot be
	// read, it is empty).
	Line int

	// Msg says what is wrong, naming the key, job or file.
	Msg string
}

// String renders the problem as `file:line: msg` (`file: msg` without a line).
func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Msg)
	}

	return fmt.Sprintf("%s: %s", p.File, p.Msg)
}

// LoadError is Load's refusal: every problem found in the file and the files it imports, not just
// the first, in file order.
type LoadError struct {
	// Problems are every problem found, sorted by file (the file given to Load first, then each
	// import in the order it was met) and line.
	Problems []Problem
}

// Error lists every problem, one `file:line: msg` per line.
func (e *LoadError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0].String()
	}

	lines := make([]string, 0, len(e.Problems)+1)
	lines = append(lines, fmt.Sprintf("%d problems in the pipeline files:", len(e.Problems)))

	for _, p := range e.Problems {
		lines = append(lines, "  "+p.String())
	}

	return strings.Join(lines, "\n")
}
