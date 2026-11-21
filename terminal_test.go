package san_ops_pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeConsole stands in for the terminal and — on every OS — a Windows console's mode, so the live
// view's choice and the mode it changes are tested without a real console.
type fakeConsole struct {
	mode   uint32
	getErr error
	setErr error
	sets   []uint32 // every mode set, in order
}

const fakeVT = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING's value

// sys is a terminal seam that calls f a terminal of 100 columns, with TERM=term.
func (c *fakeConsole) sys(f *os.File, term string) *termSys {
	fd := int(f.Fd())

	return &termSys{
		isTerminal: func(n int) bool { return n == fd },
		getSize:    func(int) (int, int, error) { return 100, 0, nil },
		getenv: func(key string) string {
			if key == "TERM" {
				return term
			}

			return ""
		},
		getMode: func(uintptr) (uint32, error) { return c.mode, c.getErr },
		setMode: func(_ uintptr, mode uint32) error {
			if c.setErr != nil {
				return c.setErr
			}

			c.sets = append(c.sets, mode)
			c.mode = mode

			return nil
		},
		vt: fakeVT,
	}
}

func viewKind(v view) string {
	switch v.(*lockedView).v.(type) {
	case *liveView:
		return "live"
	case *plainView:
		return "plain"
	default:
		return "?"
	}
}

// The live view is drawn only on an interactive terminal: never into a buffer, a nil Out, a pipe
// or a file, never with Verbose, never for TERM=dumb or a console that will not read escape codes.
func TestTheLiveViewIsChosenOnlyForAnInteractiveTerminal(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}

	defer file.Close()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	defer pr.Close()
	defer pw.Close()

	// A file x/term does not call a terminal, though its console mode reads and sets fine: the
	// terminal check alone must keep it plain.
	notTerminal := (&fakeConsole{mode: 3}).sys(file, "xterm")
	notTerminal.isTerminal = func(int) bool { return false }

	refuses := &fakeConsole{mode: 3, setErr: errors.New("the parameter is incorrect")}
	unreadable := &fakeConsole{getErr: errors.New("the handle is invalid")}

	cases := []struct {
		name string
		opts RunOptions
		sys  *termSys // nil: the real one
		want string
	}{
		{"a nil Out", RunOptions{}, nil, "plain"},
		{"a buffer", RunOptions{Out: &bytes.Buffer{}}, nil, "plain"},
		{"a pipe", RunOptions{Out: pw}, nil, "plain"},
		{"a file", RunOptions{Out: file}, nil, "plain"},
		{"a nil *os.File", RunOptions{Out: (*os.File)(nil)}, nil, "plain"},
		{"a file that is no terminal, its console mode fine", RunOptions{Out: file}, notTerminal, "plain"},
		{"a nil Out, a terminal around", RunOptions{}, (&fakeConsole{}).sys(file, "xterm"), "plain"},
		{"a buffer, a terminal around", RunOptions{Out: &bytes.Buffer{}}, (&fakeConsole{}).sys(file, "xterm"), "plain"},
		{"a terminal", RunOptions{Out: file}, (&fakeConsole{mode: 3}).sys(file, "xterm"), "live"},
		{"a terminal with Verbose", RunOptions{Out: file, Verbose: true}, (&fakeConsole{mode: 3}).sys(file, "xterm"), "plain"},
		{"a terminal with TERM=dumb", RunOptions{Out: file}, (&fakeConsole{mode: 3}).sys(file, "dumb"), "plain"},
		{"a console that refuses escape codes", RunOptions{Out: file}, refuses.sys(file, ""), "plain"},
		{"a console whose mode cannot be read", RunOptions{Out: file}, unreadable.sys(file, ""), "plain"},
	}

	for _, c := range cases {
		v := newView(c.opts, runConfig{term: c.sys})
		got := viewKind(v)
		v.close()

		if got != c.want {
			t.Errorf("%s: %s view, want %s", c.name, got, c.want)
		}
	}
}

// A run whose Out is a pipe prints plain lines — no escape code reaches it.
func TestRunPrintsPlainLinesIntoAPipe(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "show")

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	defer pr.Close()

	read := make(chan string, 1)

	go func() {
		data, _ := io.ReadAll(pr)
		read <- string(data)
	}()

	res, err := mustRun(t, mustLoad(t, root), RunOptions{Out: pw}, runConfig{})
	pw.Close()

	got := <-read

	if err != nil || res.Status != Failed {
		t.Fatalf("Run: %v %+v", err, res)
	}

	if strings.Contains(got, "\x1b") {
		t.Errorf("escape codes in a pipe:\n%q", got)
	}

	if !strings.HasPrefix(got, "pipeline show · ") || !strings.Contains(got, "\n✗ test   ") {
		t.Errorf("not the plain lines:\n%s", got)
	}
}

// The console's escape-code mode is turned on only when it is off, and put back as it was.
func TestEnableEscapeCodesPutsTheConsoleModeBack(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}

	defer file.Close()

	off := &fakeConsole{mode: 3}

	restore, err := enableEscapeCodes(*off.sys(file, ""), file.Fd())
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(off.sets) != "[7]" {
		t.Errorf("mode 3: set %v, want [7]", off.sets)
	}

	restore()

	if fmt.Sprint(off.sets) != "[7 3]" || off.mode != 3 {
		t.Errorf("mode 3 after restore: set %v (now %d), want [7 3]", off.sets, off.mode)
	}

	on := &fakeConsole{mode: 7}

	restore, err = enableEscapeCodes(*on.sys(file, ""), file.Fd())
	if err != nil {
		t.Fatal(err)
	}

	restore()

	if len(on.sets) != 0 {
		t.Errorf("mode 7 (already on): set %v, want nothing", on.sets)
	}

	for _, c := range []*fakeConsole{{getErr: errors.New("no console")}, {mode: 3, setErr: errors.New("refused")}} {
		_, err = enableEscapeCodes(*c.sys(file, ""), file.Fd())
		if err == nil {
			t.Errorf("%+v: no error", c)
		}
	}

	unix := *on.sys(file, "")
	unix.getMode, unix.setMode = nil, nil

	restore, err = enableEscapeCodes(unix, file.Fd())
	if err != nil || restore == nil {
		t.Fatalf("no console mode: %v", err)
	}

	restore()
}

// frames splits what the live view wrote into its redraws, each starting where the cursor goes back
// up to the frame's first line.
func frames(raw string) []string {
	var out []string

	for {
		i := strings.Index(raw[min(1, len(raw)):], "\r\x1b[")
		if i < 0 {
			return append(out, raw)
		}

		out = append(out, raw[:i+1])
		raw = raw[i+1:]
	}
}

// A real run on a (fake) terminal: the live view is drawn and redrawn in place — the timer's
// redraw shows the running step's last 5 lines under it — the end replaces it with the summary,
// and the console mode is put back after the run.
func TestRunDrawsTheLiveViewOnATerminalAndPutsTheConsoleModeBack(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "live")

	out, err := os.Create(filepath.Join(t.TempDir(), "terminal.txt"))
	if err != nil {
		t.Fatal(err)
	}

	defer out.Close()

	console := &fakeConsole{mode: 3}

	res, err := mustRun(t, mustLoad(t, root), RunOptions{Out: out}, runConfig{term: console.sys(out, "xterm")})
	if err != nil || res.Status != Failed {
		t.Fatalf("Run: %v %+v", err, res)
	}

	if fmt.Sprint(console.sets) != "[7 3]" || console.mode != 3 {
		t.Errorf("console modes set %v (now %d), want [7 3]: escape codes on, then the mode put back", console.sets, console.mode)
	}

	raw := readText(t, out.Name())
	scr := newVTScreen(100)

	for _, f := range frames(raw) {
		_, _ = scr.Write([]byte(f))
	}

	if len(scr.frames) < 8 {
		t.Errorf("%d redraws, want one per job and step event and more", len(scr.frames))
	}

	live := false

	for _, f := range scr.frames {
		if strings.Contains(f, "  ● test    for i in $(seq 1 7)") &&
			strings.Contains(f, "\n      line 3\n      line 4\n      line 5\n      line 6\n      line 7\n  · e2e     waiting (needs test)\n") {
			live = true
		}
	}

	if !live {
		t.Errorf("no redraw showed the running step's last 5 lines; the frames:\n%s", strings.Join(scr.frames, "\n=====\n"))
	}

	var tail strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&tail, "  line %d\n", i)
	}

	want := "pipeline live · FAILED in 0m00s\n" +
		"  ✓ build   0m00s\n" +
		"  ✗ test    0m00s   for i in $(seq 1 7); do echo \"line $i\"; done … (exit 2)\n" +
		"  ⏭ e2e     skipped (needs test)\n" +
		"  ✓ lint    0m00s\n" +
		"    ✓ vet   0m00s\n" +
		"\n" +
		"test · last lines of test.log\n" +
		tail.String() +
		"logs: .san_ops_pipeline_cache/STAMP-live/\n"

	got := normalized(scr.screen())
	if got != want {
		t.Errorf("the screen at the end:\n%s\nwant\n%s", got, want)
	}

	if len(scr.wraps) > 0 || len(scr.unknown) > 0 {
		t.Errorf("wrapped lines %v, unknown escape codes %v", scr.wraps, scr.unknown)
	}
}
