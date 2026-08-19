package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileStore is the default Store implementation. It persists receipts as an
// append-only JSON-lines journal on the local filesystem (docs/ACCORDA.md
// §21 "local journal", §42 "local history", §28 "local filesystem").
//
// Each receipt is one JSON object per line, appended with a trailing newline.
// Appending (rather than rewriting a single file) keeps the store crash-safe
// and preserves the audit-trail property: a receipt, once written, is never
// mutated. List reads the journal back in append order.
//
// FileStore adds no dependency beyond the standard library, honoring the
// minimal-dependency rule (docs/DECISIONS.md #1).
type FileStore struct {
	// path is the journal file path.
	path string
}

// NewFileStore returns a Store that persists receipts to the journal file at
// path. The parent directory is created on first Append.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// Append records r by appending a single JSON line to the journal. It creates
// the parent directory if needed and opens the file in append mode so
// concurrent appends from a single process are serialized by the OS. The
// write is flushed before returning so a completed Append is durable.
func (s *FileStore) Append(_ context.Context, r Receipt) error {
	if s == nil || s.path == "" {
		return errors.New("history: file store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("history: create store dir: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("history: open journal: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("history: marshal receipt: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("history: write receipt: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("history: sync receipt: %w", err)
	}
	return nil
}

// List returns all receipts in the journal in append order (oldest first).
// A missing journal file is treated as an empty history, not an error, so a
// fresh deployment can list history before any receipt exists.
func (s *FileStore) List(_ context.Context) ([]Receipt, error) {
	if s == nil || s.path == "" {
		return nil, errors.New("history: file store path is empty")
	}
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("history: open journal: %w", err)
	}
	defer f.Close()

	var receipts []Receipt
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Receipt
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("history: decode receipt: %w", err)
		}
		receipts = append(receipts, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("history: read journal: %w", err)
	}
	return receipts, nil
}
