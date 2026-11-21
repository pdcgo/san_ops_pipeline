package san_ops_pipeline

import (
	"io"
	"os"
)

// termSys is the outside world the live view needs: whether a file is an interactive terminal,
// its size, the environment, and — on Windows only — the console's mode. defaultTermSys gives the
// real one (terminal_windows.go, terminal_other.go); a test passes its own through runConfig, so no
// test depends on a real console.
type termSys struct {
	isTerminal func(fd int) bool
	getSize    func(fd int) (width, height int, err error)
	getenv     func(key string) string

	// getMode and setMode read and set a Windows console's output mode; nil elsewhere, where a
	// terminal reads escape codes as it is. vt is the mode bit that makes the console read them
	// (ENABLE_VIRTUAL_TERMINAL_PROCESSING).
	getMode func(fd uintptr) (uint32, error)
	setMode func(fd uintptr, mode uint32) error
	vt      uint32
}

// terminal is an interactive terminal a run draws its live view on.
type terminal struct {
	w io.Writer

	// size is the terminal's size now (it is read again before every redraw, so a resized window
	// is followed); width 80 and height 0 (unknown) when it cannot be read.
	size func() (width, height int)

	// restore puts back what openTerminal changed — the Windows console mode. Called once.
	restore func()
}

// openTerminal returns the terminal behind out, or false when out is not one: not an *os.File
// (a buffer, a nil Out), a file or pipe (a redirect, cron, a bot's shell, Git Bash's mintty window,
// which gives programs a pipe), TERM=dumb, or a Windows console that will not read escape codes
// (before Windows 10). Only then does a run draw the live view; anything else gets plain lines.
//
// On Windows it turns on the console's escape-code mode (ENABLE_VIRTUAL_TERMINAL_PROCESSING) when
// it is off; the returned terminal's restore puts the mode back.
func openTerminal(out io.Writer, sys termSys) (*terminal, bool) {
	f, ok := out.(*os.File)
	if !ok || f == nil {
		return nil, false
	}

	fd := f.Fd()

	if !sys.isTerminal(int(fd)) {
		return nil, false
	}

	// Build choice (to confirm): TERM=dumb (Emacs' shell, some editors' panes) means a terminal
	// that cannot move the cursor, so it gets plain lines.
	if sys.getenv("TERM") == "dumb" {
		return nil, false
	}

	restore, err := enableEscapeCodes(sys, fd)
	if err != nil {
		return nil, false
	}

	size := func() (int, int) {
		w, h, err := sys.getSize(int(fd))
		if err != nil || w <= 0 {
			return 80, 0
		}

		return w, h
	}

	return &terminal{w: f, size: size, restore: restore}, true
}

// enableEscapeCodes makes a Windows console read escape codes: it reads the console's mode and,
// when the vt bit is off, sets it. The returned function puts the mode back as it was. Where there
// is no console mode (unix) — or the bit is already on (Windows Terminal) — nothing changes and
// the function does nothing. An error means the console cannot read escape codes.
func enableEscapeCodes(sys termSys, fd uintptr) (restore func(), err error) {
	nothing := func() {}

	if sys.getMode == nil || sys.setMode == nil {
		return nothing, nil
	}

	mode, err := sys.getMode(fd)
	if err != nil {
		return nil, err
	}

	if mode&sys.vt != 0 {
		return nothing, nil
	}

	err = sys.setMode(fd, mode|sys.vt)
	if err != nil {
		return nil, err
	}

	return func() {
		// Nothing more can be done when it fails: the run is over and its output written.
		_ = sys.setMode(fd, mode)
	}, nil
}
