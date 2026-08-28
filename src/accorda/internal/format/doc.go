// Package format provides shared, terminal-aware styling for the Accorda CLI
// (docs/ACCORDA.md §11). It centralizes the ANSI color codes and a Style that
// emits them only when writing to a real terminal, so piped output and tests
// (which write to buffers) stay plain. status, history, doctor, and the
// per-target headers all use it to keep human-facing output readable without
// coupling the command layer to a terminal library.
package format
