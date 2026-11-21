//go:build !windows

package san_ops_pipeline

import (
	"os"

	"golang.org/x/term"
)

// defaultTermSys is the real terminal off Windows: x/term for the check and the size. A unix
// terminal reads escape codes as it is, so there is no mode to set.
func defaultTermSys() termSys {
	return termSys{
		isTerminal: term.IsTerminal,
		getSize:    term.GetSize,
		getenv:     os.Getenv,
	}
}
