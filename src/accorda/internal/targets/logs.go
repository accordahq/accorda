package targets

import (
	"context"
	"io"
)

// LogOptions controls how a target fetches service logs
// (docs/ACCORDA.md §11).
type LogOptions struct {
	// Follow keeps the log stream open and writes new records until the
	// context is cancelled or the target closes the stream.
	Follow bool
	// Tail selects how many records to return. Target drivers accept the
	// target-native "all" value as well as a decimal line count.
	Tail string
}

// LogTarget is the optional operational capability implemented by targets
// that can fetch or stream service logs (docs/ACCORDA.md §11). Logging is
// separate from Target because it is not part of the reconciliation lifecycle
// described by the five-method interface in §12.
type LogTarget interface {
	Logs(ctx context.Context, service string, opts LogOptions, stdout, stderr io.Writer) error
}
