//go:build windows

package san_ops_pipeline

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows the real seam reads and sets the console's mode; a pipe is no console — x/term says so,
// and GetConsoleMode refuses it — so even a check that wrongly said "terminal" ends in plain lines,
// the mode untouched.
func TestTheWindowsSeamUsesTheConsoleMode(t *testing.T) {
	t.Parallel()

	sys := defaultTermSys()
	if sys.getMode == nil || sys.setMode == nil {
		t.Fatal("the Windows seam has no console mode")
	}

	if sys.vt != windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING {
		t.Errorf("vt bit %#x, want ENABLE_VIRTUAL_TERMINAL_PROCESSING", sys.vt)
	}

	var fake uint32 = fakeVT
	if fake != sys.vt {
		t.Errorf("the tests' fake vt bit %#x is not Windows' %#x", fake, sys.vt)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	defer pr.Close()
	defer pw.Close()

	if sys.isTerminal(int(pw.Fd())) {
		t.Error("x/term calls a pipe a terminal")
	}

	_, err = sys.getMode(pw.Fd())
	if err == nil {
		t.Error("GetConsoleMode read a mode from a pipe")
	}

	forced := sys
	forced.isTerminal = func(int) bool { return true }

	_, ok := openTerminal(pw, forced)
	if ok {
		t.Error("a pipe got the live view")
	}
}
