package history

import (
	"context"
	"errors"
)

// Store persists deployment receipts so Accorda can prove what was deployed
// and answer history queries (docs/ACCORDA.md §7, §42 "local history"). It is
// the seam core depends on; the concrete implementation lives behind this
// interface so core never knows where receipts are written.
type Store interface {
	// Append records a receipt. It must be safe for concurrent use and must
	// not return until the receipt is durably persisted.
	Append(ctx context.Context, r Receipt) error
	// List returns all recorded receipts in the order they were appended
	// (oldest first).
	List(ctx context.Context) ([]Receipt, error)
}

// ErrNotImplemented is returned by stub stores for methods that have no
// backing implementation yet.
var ErrNotImplemented = errNotImplemented{}

type errNotImplemented struct{}

func (errNotImplemented) Error() string { return "history: not implemented" }

// Compile-time interface check: the Stub type verifies the Store interface
// here so a missing method is caught at build time, not at runtime.
var _ Store = (*Stub)(nil)

// Stub is a Store implementation that returns ErrNotImplemented for every
// method. It exists so core code and tests can reference a Store without a
// concrete implementation, and so the Store interface has a concrete
// implementation guarding it at compile time.
type Stub struct{}

// NewStub returns a no-op Store.
func NewStub() *Stub { return &Stub{} }

func (Stub) Append(context.Context, Receipt) error { return ErrNotImplemented }
func (Stub) List(context.Context) ([]Receipt, error) {
	return nil, ErrNotImplemented
}

// ErrNotFound is returned by stores when a requested receipt does not exist.
var ErrNotFound = errors.New("history: receipt not found")
