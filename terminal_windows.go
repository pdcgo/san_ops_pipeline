//go:build windows

package san_ops_pipeline

import (
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// defaultTermSys is the real terminal on Windows: x/term for the check and the size, and the
// console's output mode through x/sys/windows — cmd and PowerShell's console read escape codes only
// once ENABLE_VIRTUAL_TERMINAL_PROCESSING is set (Windows 10 and later).
func defaultTermSys() termSys {
	return termSys{
		isTerminal: term.IsTerminal,
		getSize:    term.GetSize,
		getenv:     os.Getenv,
		getMode:    getConsoleMode,
		setMode:    setConsoleMode,
		vt:         windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING,
	}
}

func getConsoleMode(fd uintptr) (uint32, error) {
	var mode uint32

	err := windows.GetConsoleMode(windows.Handle(fd), &mode)

	return mode, err
}

func setConsoleMode(fd uintptr, mode uint32) error {
	return windows.SetConsoleMode(windows.Handle(fd), mode)
}
