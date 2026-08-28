package format

import (
	"bytes"
	"os"
	"testing"
)

// TestStyle_PaintDisabledForBuffer verifies a Style over a non-terminal writer
// (a buffer) leaves text unchanged, so tests and piped output stay plain.
func TestStyle_PaintDisabledForBuffer(t *testing.T) {
	var buf bytes.Buffer
	st := NewStyle(&buf)
	if st.Enabled() {
		t.Fatal("Style over a buffer should be disabled")
	}
	if got := st.Paint("SYNCED", Green); got != "SYNCED" {
		t.Errorf("Paint over buffer = %q, want plain text", got)
	}
}

// TestStyle_PaintEnabledForTerminal verifies an enabled Style wraps text in the
// requested color and resets it. The enabled path is forced directly because
// os.Stdout is a pipe under CI, not a character device.
func TestStyle_PaintEnabledForTerminal(t *testing.T) {
	st := &Style{enabled: true}
	if got := st.Paint("SYNCED", Green); got != Green+"SYNCED"+Reset {
		t.Errorf("Paint enabled = %q, want wrapped in green", got)
	}
}

// TestStyle_PaintEmptyColor verifies an empty color leaves text unchanged even
// when styling is enabled, so placeholder cells (e.g. health "-") stay plain.
func TestStyle_PaintEmptyColor(t *testing.T) {
	st := &Style{enabled: true}
	if got := st.Paint("healthy", ""); got != "healthy" {
		t.Errorf("Paint with empty color = %q, want unchanged", got)
	}
}

// TestIsTerminal_NonFileWriter verifies a non-*os.File writer is not a terminal.
func TestIsTerminal_NonFileWriter(t *testing.T) {
	var buf bytes.Buffer
	if IsTerminal(&buf) {
		t.Error("IsTerminal(buffer) = true, want false")
	}
}

// TestIsTerminal_RegularFile verifies a regular file is not a terminal.
func TestIsTerminal_RegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("IsTerminal(regular file) = true, want false")
	}
}
