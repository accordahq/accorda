package format

import (
	"io"
	"os"
)

// IsTerminal reports whether w is a character device (a terminal). It returns
// false for buffers, regular files, and pipes, so callers can disable styling
// when the output is not a human-facing terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
