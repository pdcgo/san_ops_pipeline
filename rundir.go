package san_ops_pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cacheDirName is the folder under the root every run writes into (owner: "run folder is
// ./.san_ops_pipeline_cache/*").
const cacheDirName = ".san_ops_pipeline_cache"

// stampLayout stamps a run folder in local time, as `san test` stamps its runs.
const stampLayout = "20060102-150405"

// makeRunDir creates `<root>/.san_ops_pipeline_cache/<stamp>-<name>/` and returns it. When it
// creates the cache folder itself it writes a `.gitignore` holding `*` into it, so no repo has to
// list the folder (the file ignores itself too). A folder of the same name — two runs in the same
// second — gets `-2`, `-3`, … after it; os.Mkdir claims a name atomically, so two runs never share one.
func makeRunDir(root, stamp, name string) (string, error) {
	cache := filepath.Join(root, cacheDirName)

	err := os.Mkdir(cache, 0o755)
	if err == nil {
		err = os.WriteFile(filepath.Join(cache, ".gitignore"), []byte("*\n"), 0o644)
		if err != nil {
			return "", fmt.Errorf("san_ops_pipeline: write %s/.gitignore: %w", cacheDirName, err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("san_ops_pipeline: create the run folder: %w", err)
	}

	base := stamp + "-" + safeName(name)

	for n := 1; n <= 1000; n++ {
		dir := filepath.Join(cache, base)
		if n > 1 {
			dir += "-" + strconv.Itoa(n)
		}

		err = os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}

		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("san_ops_pipeline: create the run folder: %w", err)
		}
	}

	return "", fmt.Errorf("san_ops_pipeline: create the run folder: %s-2 … -1000 all exist", base)
}

// safeName makes a pipeline name usable as part of a folder name on every system: letters, digits,
// `.`, `_` and `-` stay, anything else becomes `-` (runs of them one `-`), no `-` or `.` at either
// end, at most 60 characters; nothing left → `pipeline`.
func safeName(name string) string {
	var b strings.Builder

	dash := false

	for _, r := range name {
		ok := r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')

		if !ok || r == '-' {
			if !dash {
				b.WriteByte('-')
			}

			dash = true

			continue
		}

		b.WriteRune(r)
		dash = false
	}

	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}

	s = strings.Trim(s, "-.")
	if s == "" {
		return "pipeline"
	}

	return s
}

// summaryMarkdown renders summary.md: the pipeline, its result, and each job — passed, failed,
// skipped — with its time and log; an imported file's jobs nested under the job that imports it.
func summaryMarkdown(res *Result, running string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# pipeline %s — %s\n\n", res.Name, strings.ToUpper(string(res.Status)))
	fmt.Fprintf(&b, "- file: %s\n", codeSpan(res.File))
	fmt.Fprintf(&b, "- started: %s\n", res.Started.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- took: %s\n", formatDuration(res.Took))

	if res.Status == Interrupted {
		where := "between two jobs"
		if running != "" {
			where = "while " + running + " ran"
		}

		fmt.Fprintf(&b, "- interrupted: the run was stopped (Ctrl-C or the caller's ctx) %s; nothing after it started\n", where)
	}

	b.WriteString("\n## Jobs\n\n")
	summaryJobs(&b, res.Jobs, "")

	return b.String()
}

func summaryJobs(b *strings.Builder, jobs []JobResult, indent string) {
	for _, j := range jobs {
		fmt.Fprintf(b, "%s- %s **%s** · %s", indent, statusMark(j.Status), j.Name, j.Status)

		switch j.Status {
		case Skipped:
			fmt.Fprintf(b, " (needs %s)", j.Needs)
		case NotRun:
		default:
			fmt.Fprintf(b, " · %s", formatDuration(j.Took))
		}

		if j.Step > 0 && j.Status != Passed {
			fmt.Fprintf(b, " · step %d %s", j.Step, codeSpan(j.Command))

			switch {
			case j.Reason != "":
				fmt.Fprintf(b, " could not start: %s", j.Reason)
			case j.Status == Failed:
				fmt.Fprintf(b, " exit %d", j.Exit)
			}
		}

		if j.Uses != "" {
			fmt.Fprintf(b, " · uses %s", codeSpan(j.Uses))
		}

		if j.Log != "" {
			fmt.Fprintf(b, " · [%s](%s)", j.Log, j.Log)
		}

		b.WriteString("\n")

		if len(j.Jobs) > 0 {
			summaryJobs(b, j.Jobs, indent+"  ")
		}
	}
}

// statusMark is the symbol a status is shown with.
func statusMark(s Status) string {
	switch s {
	case Passed:
		return "✓"
	case Failed, Interrupted:
		return "✗"
	case Skipped:
		return "⏭"
	default:
		return "·"
	}
}

// codeSpan writes a command's first line as Markdown code (`…` after it when it has more lines).
func codeSpan(s string) string {
	s = firstLine(s)
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}

	return "`" + s + "`"
}

// firstLine is a command's first non-empty line, with ` …` when more lines follow.
func firstLine(s string) string {
	s = strings.TrimSpace(s)

	line, rest, more := strings.Cut(s, "\n")
	line = strings.TrimRight(line, "\r")

	if more && strings.TrimSpace(rest) != "" {
		return line + " …"
	}

	return line
}

// writeSummary writes summary.md into the run folder.
func writeSummary(res *Result, running string) error {
	err := os.WriteFile(filepath.Join(res.Dir, "summary.md"), []byte(summaryMarkdown(res, running)), 0o644)
	if err != nil {
		return fmt.Errorf("san_ops_pipeline: write summary.md: %w", err)
	}

	return nil
}

// stamp is the run folder's time stamp.
func stamp(t time.Time) string {
	return t.Format(stampLayout)
}
