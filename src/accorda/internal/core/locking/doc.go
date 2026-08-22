// Package locking provides target-scoped deployment locks for reconciliation.
// Locks serialize complete reconciliation cycles across processes and recover
// automatically when the process that owned a lock exits unexpectedly
// (docs/ACCORDA.md §47).
package locking
