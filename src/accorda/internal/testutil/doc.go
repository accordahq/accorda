// Package testutil provides shared helpers for integration tests that
// exercise Accorda against real external dependencies: a Git repository, a
// Docker daemon, and the Docker Compose CLI (docs/ACCORDA.md §55).
//
// Integration tests are gated behind the `integration` build tag and skip
// gracefully when a required dependency is unavailable, so the default
// `go test ./...` run stays hermetic. The helpers here are shared by the git
// source, compose target, and end-to-end integration suites so repository
// fixtures and prerequisite checks are not duplicated across packages
// (docs/DECISIONS.md #8 DRY).
package testutil
