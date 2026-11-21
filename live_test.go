package san_ops_pipeline

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock is the live view's clock in the rendering tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

var liveT0 = time.Date(2026, 11, 21, 14, 30, 5, 0, time.Local)

// liveOnScreen is a live view drawing on a fake terminal, with a fake clock.
type liveOnScreen struct {
	v     *liveView
	scr   *vtScreen
	clock *fakeClock
	p     *Pipeline
	res   *Result
}

func newLiveOnScreen(t *testing.T, root string, width, height int) *liveOnScreen {
	t.Helper()

	p := mustLoad(t, root)
	scr := newVTScreen(width)
	scr.height = height
	clock := &fakeClock{t: liveT0}

	v := newLiveView(&terminal{
		w:       scr,
		size:    func() (int, int) { return width, height },
		restore: func() {},
	})
	v.now = clock.now

	res := &Result{
		Name:    p.Name,
		File:    p.File,
		Dir:     filepath.Join(p.root, cacheDirName, "20261121-143005-"+p.Name),
		Started: liveT0,
	}

	return &liveOnScreen{v: v, scr: scr, clock: clock, p: p, res: res}
}

func ref(path string) jobRef {
	return jobRef{Path: path}
}

func wantScreen(t *testing.T, label string, s *vtScreen, want string) {
	t.Helper()

	got := s.screen()
	if got != want {
		t.Errorf("%s — the screen shows\n%s\nwant\n%s", label, got, want)
	}

	if len(s.wraps) > 0 {
		t.Errorf("%s — lines wrapped: %v", label, s.wraps)
	}

	if len(s.unknown) > 0 {
		t.Errorf("%s — escape codes the view should not send: %v", label, s.unknown)
	}
}

// The whole life of a live view, through a fake terminal: laid out at the start (the imported
// file's job indented under the job that imports it), redrawn in place as jobs run — the running
// step's last 5 lines under it, the timer ticking — and replaced at the end by the summary, the
// failing step's last 20 lines and the run folder (the record's mocks).
func TestLiveViewDrawsTheRunInPlace(t *testing.T) {
	t.Parallel()

	l := newLiveOnScreen(t, "testdata/show", 80, 0)
	v, test := l.v, l.p.Jobs[1]

	v.runStart(l.p, l.res)
	wantScreen(t, "at the start", l.scr, `pipeline show · 20261121-143005-show   0m00s
  · build   waiting
  · test    waiting (needs build)
  · e2e     waiting (needs test)
  · lint    waiting
    · vet   waiting
`)

	v.jobStart(ref("build"))
	v.stepStart(ref("build"), 1, l.p.Jobs[0].Steps[0])
	v.output(ref("build"), "built")
	l.clock.add(2 * time.Second)
	v.jobEnd(ref("build"), &JobResult{Name: "build", Status: Passed, Took: 2 * time.Second, Log: "build.log"}, nil)

	v.jobStart(ref("test"))
	v.stepStart(ref("test"), 1, test.Steps[0])
	v.output(ref("test"), "ok")
	l.clock.add(time.Second)
	v.stepStart(ref("test"), 2, test.Steps[1])

	for i := 1; i <= 7; i++ {
		v.output(ref("test"), fmt.Sprintf("line %d", i))
	}

	l.clock.add(time.Second)
	v.tick(ref("test"), time.Second)
	wantScreen(t, "while test runs", l.scr, `pipeline show · 20261121-143005-show                                0m04s
  ✓ build   echo built                                              0m02s
  ● test    2/2 · for i in $(seq 1 25); do echo "line $i"; done …   0m02s
      line 3
      line 4
      line 5
      line 6
      line 7
  · e2e     waiting (needs test)
  · lint    waiting
    · vet   waiting
`)

	var tail []string
	for i := 6; i <= 25; i++ {
		tail = append(tail, fmt.Sprintf("line %d", i))
	}

	l.clock.add(time.Second)
	v.jobEnd(ref("test"), &JobResult{
		Name: "test", Status: Failed, Took: 3 * time.Second, Log: "test.log",
		Step: 2, Command: test.Steps[1].Run, Exit: 2,
	}, tail)
	v.jobEnd(ref("e2e"), &JobResult{Name: "e2e", Status: Skipped, Needs: "test"}, nil)

	v.jobStart(ref("lint"))
	v.jobStart(ref("lint/vet"))
	v.stepStart(ref("lint/vet"), 1, l.p.Jobs[3].Import.Jobs[0].Steps[0])
	v.output(ref("lint/vet"), "vetting")
	l.clock.add(time.Second)
	v.tick(ref("lint/vet"), time.Second)
	wantScreen(t, "while the imported job runs", l.scr, `pipeline show · 20261121-143005-show                                   0m06s
  ✓ build   echo built                                                 0m02s
  ✗ test    for i in $(seq 1 25); do echo "line $i"; done … (exit 2)   0m03s
  ⏭ e2e     skipped (needs test)
  ● lint    uses pipeline/lint.yaml                                    0m01s
    ● vet   echo vetting                                               0m01s
        vetting
`)

	vet := JobResult{Name: "vet", Status: Passed, Took: time.Second, Log: "lint/vet.log"}
	v.jobEnd(ref("lint/vet"), &vet, nil)

	lint := JobResult{Name: "lint", Status: Passed, Took: time.Second, Uses: "pipeline/lint.yaml", Jobs: []JobResult{vet}}
	v.jobEnd(ref("lint"), &lint, nil)

	l.res.Status = Failed
	l.res.Took = 6 * time.Second
	l.res.Jobs = []JobResult{
		{Name: "build", Status: Passed, Took: 2 * time.Second, Log: "build.log"},
		{Name: "test", Status: Failed, Took: 3 * time.Second, Log: "test.log", Step: 2, Command: test.Steps[1].Run, Exit: 2},
		{Name: "e2e", Status: Skipped, Needs: "test"},
		lint,
	}
	v.runEnd(l.res)

	var last strings.Builder
	for _, line := range tail {
		fmt.Fprintf(&last, "  %s\n", line)
	}

	wantScreen(t, "at the end", l.scr, "pipeline show · FAILED in 0m06s\n"+
		"  ✓ build   0m02s\n"+
		"  ✗ test    0m03s   for i in $(seq 1 25); do echo \"line $i\"; done … (exit 2)\n"+
		"  ⏭ e2e     skipped (needs test)\n"+
		"  ✓ lint    0m01s\n"+
		"    ✓ vet   0m01s\n"+
		"\n"+
		"test · last lines of test.log\n"+
		last.String()+
		"logs: .san_ops_pipeline_cache/20261121-143005-show/\n")
}

// The record's mocks (`the-terminal-shows-a-live-view`), drawn by the code — the README shows these
// same frames, with an imported job's own jobs indented under it (default 14).
func TestLiveViewDrawsTheRecordsMocks(t *testing.T) {
	t.Parallel()

	step := func(run string) []*Step { return []*Step{{Run: run}} }
	checks := &Pipeline{Name: "checks", Jobs: []*Job{
		{Name: "build", Steps: step("go build ./...")},
		{Name: "vet", Steps: step("go vet ./...")},
	}}
	p := &Pipeline{Name: "slice", root: t.TempDir(), Jobs: []*Job{
		{Name: "checks", Uses: "pipeline/checks.yaml", Import: checks},
		{Name: "go-tests", Needs: []string{"checks"}, Steps: step("go test ./backend/...")},
		{Name: "stories", Needs: []string{"checks"}, Steps: step("npm run test:stories")},
		{Name: "e2e", Needs: []string{"go-tests"}, Steps: step("go run ./tools/san test affected --run")},
	}}

	scr := newVTScreen(120)
	clock := &fakeClock{t: liveT0}
	v := newLiveView(&terminal{w: scr, size: func() (int, int) { return 120, 0 }, restore: func() {}})
	v.now = clock.now

	res := &Result{Name: "slice", Dir: filepath.Join(p.root, cacheDirName, "20261121-143005-slice"), Started: liveT0}
	v.runStart(p, res)

	build := JobResult{Name: "build", Status: Passed, Took: 40 * time.Second, Log: "checks/build.log"}
	vet := JobResult{Name: "vet", Status: Passed, Took: 22 * time.Second, Log: "checks/vet.log"}
	imported := JobResult{Name: "checks", Status: Passed, Took: 62 * time.Second, Uses: "pipeline/checks.yaml", Jobs: []JobResult{build, vet}}

	v.jobStart(ref("checks"))
	v.jobStart(ref("checks/build"))
	v.stepStart(ref("checks/build"), 1, checks.Jobs[0].Steps[0])
	clock.add(40 * time.Second)
	v.jobEnd(ref("checks/build"), &build, nil)
	v.jobStart(ref("checks/vet"))
	v.stepStart(ref("checks/vet"), 1, checks.Jobs[1].Steps[0])
	clock.add(22 * time.Second)
	v.jobEnd(ref("checks/vet"), &vet, nil)
	v.jobEnd(ref("checks"), &imported, nil)

	v.jobStart(ref("go-tests"))
	v.stepStart(ref("go-tests"), 1, p.Jobs[1].Steps[0])
	v.output(ref("go-tests"), "ok   …/order_service/order_v1        41.2s")
	v.output(ref("go-tests"), "ok   …/inventory_service/inventory_v1 58.7s")
	v.output(ref("go-tests"), "=== RUN   TestRestockAccept")
	clock.add(338 * time.Second)
	v.tick(ref("go-tests"), 338*time.Second)

	wantScreen(t, "while it runs", scr, `pipeline slice · 20261121-143005-slice     6m40s
  ✓ checks     uses pipeline/checks.yaml   1m02s
    ✓ build   go build ./...               0m40s
    ✓ vet     go vet ./...                 0m22s
  ● go-tests   go test ./backend/...       5m38s
      ok   …/order_service/order_v1        41.2s
      ok   …/inventory_service/inventory_v1 58.7s
      === RUN   TestRestockAccept
  · stories    waiting
  · e2e        waiting (needs go-tests)
`)

	goTests := JobResult{
		Name: "go-tests", Status: Failed, Took: 338 * time.Second, Log: "go-tests.log",
		Step: 1, Command: "go test ./backend/...", Exit: 1,
	}
	v.jobEnd(ref("go-tests"), &goTests, []string{
		"--- FAIL: TestRestockAccept (0.42s)",
		"    restock_accept_test.go:88: want 3 units, got 2",
	})

	stories := JobResult{Name: "stories", Status: Passed, Took: 435 * time.Second, Log: "stories.log"}
	v.jobStart(ref("stories"))
	v.jobEnd(ref("stories"), &stories, nil)

	e2e := JobResult{Name: "e2e", Status: Skipped, Needs: "go-tests"}
	v.jobEnd(ref("e2e"), &e2e, nil)

	res.Status = Failed
	res.Took = 838 * time.Second
	res.Jobs = []JobResult{imported, goTests, stories, e2e}
	v.runEnd(res)

	wantScreen(t, "when it ends", scr, `pipeline slice · FAILED in 13m58s
  ✓ checks     1m02s
    ✓ build   0m40s
    ✓ vet     0m22s
  ✗ go-tests   5m38s   go test ./backend/... (exit 1)
  ✓ stories    7m15s
  ⏭ e2e        skipped (needs go-tests)

go-tests · last lines of go-tests.log
  --- FAIL: TestRestockAccept (0.42s)
      restock_accept_test.go:88: want 3 units, got 2
logs: .san_ops_pipeline_cache/20261121-143005-slice/
`)
}

// Every line of a redraw is cut to the terminal's width minus one column — a long command, a long
// output line, a step's own escape codes, tabs and `\r` progress — so a redraw never wraps.
func TestLiveViewCutsEveryLineToTheTerminalWidth(t *testing.T) {
	t.Parallel()

	l := newLiveOnScreen(t, "testdata/show", 40, 0)
	v, test := l.v, l.p.Jobs[1]

	v.runStart(l.p, l.res)
	v.jobStart(ref("build"))
	v.stepStart(ref("build"), 1, test.Steps[1])
	v.output(ref("build"), strings.Repeat("0123456789", 10))
	v.output(ref("build"), "\x1b[31mred\x1b[0m\tafter a tab\x1b[2J")
	v.output(ref("build"), "10%\r55%\r100% done")
	v.output(ref("build"), "日本語のテキストはそれぞれ二つの列を使います")
	v.tick(ref("build"), time.Second)

	wantScreen(t, "a narrow terminal", l.scr, `pipeline show · 20261121-14300…   0m00s
  ● build   for i in $(seq 1 2…   0m00s
      01234567890123456789012345678901…
      red     after a tab
      100% done
      日本語のテキストはそれぞれ二つの…
  · test    waiting (needs build)
  · e2e     waiting (needs test)
  · lint    waiting
    · vet   waiting
`)

	for _, line := range strings.Split(strings.TrimSuffix(l.scr.screen(), "\n"), "\n") {
		if cells(line) > 39 {
			t.Errorf("%q takes %d columns, more than the terminal's 40 less one", line, cells(line))
		}
	}
}

// A frame taller than the terminal cannot be drawn again in place; it keeps the header and the
// running job with its lines in sight, saying how many lines are left out above and below.
func TestLiveViewKeepsTheRunningJobInSightOnAShortTerminal(t *testing.T) {
	t.Parallel()

	jobs := make([]*Job, 0, 12)
	for i := 1; i <= 12; i++ {
		jobs = append(jobs, &Job{Name: fmt.Sprintf("job%02d", i), Steps: []*Step{{Run: "make it"}}})
	}

	p := &Pipeline{Name: "many", Jobs: jobs, root: t.TempDir()}

	scr := newVTScreen(60)
	scr.height = 10
	clock := &fakeClock{t: liveT0}

	v := newLiveView(&terminal{w: scr, size: func() (int, int) { return 60, 10 }, restore: func() {}})
	v.now = clock.now
	v.runStart(p, &Result{Name: "many", Dir: filepath.Join(p.root, cacheDirName, "20261121-143005-many"), Started: liveT0})

	for i := 1; i <= 7; i++ {
		name := fmt.Sprintf("job%02d", i)
		v.jobStart(ref(name))
		v.stepStart(ref(name), 1, jobs[i-1].Steps[0])

		if i < 7 {
			v.jobEnd(ref(name), &JobResult{Name: name, Status: Passed, Log: name + ".log"}, nil)
		}
	}

	for i := 1; i <= 3; i++ {
		v.output(ref("job07"), fmt.Sprintf("output %d", i))
	}

	v.tick(ref("job07"), time.Second)

	wantScreen(t, "a terminal 10 lines high", scr, `pipeline many · 20261121-143005-many   0m00s
  … 4 more above
  ✓ job05   make it                    0m00s
  ✓ job06   make it                    0m00s
  ● job07   make it                    0m00s
      output 1
      output 2
      output 3
  … 5 more below
`)
}

// Output alone never redraws; the timer redraws at most once a second of a step's running time,
// however often the runner ticks — and a new step starts its own second.
func TestLiveViewRedrawsTheTimerAtMostOnceASecond(t *testing.T) {
	t.Parallel()

	l := newLiveOnScreen(t, "testdata/slow", 80, 0)
	v, slow := l.v, l.p.Jobs[0]

	v.runStart(l.p, l.res)
	v.jobStart(ref("slow"))
	v.stepStart(ref("slow"), 1, slow.Steps[0])

	start := l.scr.writes

	for i := 0; i < 50; i++ {
		v.output(ref("slow"), "a line")
	}

	if l.scr.writes != start {
		t.Fatalf("output lines redrew the view %d times", l.scr.writes-start)
	}

	var redrawnAt []time.Duration

	for running := 200 * time.Millisecond; running <= 2200*time.Millisecond; running += 200 * time.Millisecond {
		before := l.scr.writes
		v.tick(ref("slow"), running)

		if l.scr.writes != before {
			redrawnAt = append(redrawnAt, running)
		}
	}

	if fmt.Sprint(redrawnAt) != "[1s 2s]" {
		t.Errorf("ticks every 200ms redrew at %v, want [1s 2s]", redrawnAt)
	}

	v.stepStart(ref("slow"), 1, slow.Steps[0])

	before := l.scr.writes
	v.tick(ref("slow"), time.Second)

	if l.scr.writes != before+1 {
		t.Errorf("the next step's first second did not redraw")
	}
}

func TestScreenText(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain":                        "plain",
		"\x1b[1;31mred\x1b[0m":         "red",
		"50%\r75%\r100%":               "100%",
		"a\tb":                         "a       b",
		"abcdefgh\tz":                  "abcdefgh        z",
		"\x1b]0;a title\x07after":      "after",
		"\x1b]8;;http://x\x1b\\link":   "link",
		"bell\x07 and backspace\x08":   "bell and backspace",
		"\x1b7saved\x1b8":              "saved",
		"日本\tx":                        "日本    x",
		"an unfinished escape \x1b[31": "an unfinished escape ",
	}

	for in, want := range cases {
		got := screenText(in)
		if got != want {
			t.Errorf("screenText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCells(t *testing.T) {
	t.Parallel()

	widths := map[string]int{
		"abc":          3,
		"✓ ✗ ⏭ ● · …":  11,
		"日本語":          6,
		"한국어":          6,
		"e\u0301":      1,
		"🚀":            2,
		"ｆｕｌｌ":         8,
		"":             0,
		"zero\u200bjs": 6,
	}

	for s, want := range widths {
		got := cells(s)
		if got != want {
			t.Errorf("cells(%q) = %d, want %d", s, got, want)
		}
	}

	cuts := []struct {
		s    string
		n    int
		want string
	}{
		{"abcdef", 10, "abcdef"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"日本語テキスト", 7, "日本語…"},
		{"日本語テキスト", 6, "日本…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	}

	for _, c := range cuts {
		got := cutCells(c.s, c.n)
		if got != c.want || cells(got) > c.n {
			t.Errorf("cutCells(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}
