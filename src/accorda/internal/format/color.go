package format

import "io"

// ANSI SGR color codes used by the CLI. They are emitted only when a Style is
// enabled (writing to a terminal); otherwise Paint returns the input unchanged.
const (
	Reset   = "\x1b[0m"
	Red     = "\x1b[31m"
	Green   = "\x1b[32m"
	Yellow  = "\x1b[33m"
	Blue    = "\x1b[34m"
	Magenta = "\x1b[35m"
	Cyan    = "\x1b[36m"
)

// Style applies ANSI styling to strings only when writing to a terminal. It
// is constructed from the destination writer so piped output and test buffers
// (which are not character devices) render plain text.
type Style struct {
	enabled bool
}

// NewStyle returns a Style that emits ANSI codes when w is a terminal.
func NewStyle(w io.Writer) *Style {
	return &Style{enabled: IsTerminal(w)}
}

// Enabled reports whether styling is active for the writer.
func (s *Style) Enabled() bool { return s.enabled }

// Paint wraps text in color when styling is enabled and color is non-empty;
// otherwise it returns text unchanged so non-terminal output stays plain.
func (s *Style) Paint(text, color string) string {
	if !s.enabled || color == "" {
		return text
	}
	return color + text + Reset
}
