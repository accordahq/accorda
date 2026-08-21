package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"accorda/internal/core/history"
)

// memStore is an in-memory history.Store for unit testing the history and
// inspect commands without touching the filesystem. It records the receipts
// it was given so collectHistory/collectInspect can read them back.
type memStore struct {
	receipts []history.Receipt
}

func (m *memStore) Append(_ context.Context, r history.Receipt) error {
	m.receipts = append(m.receipts, r)
	return nil
}

func (m *memStore) List(_ context.Context) ([]history.Receipt, error) {
	return m.receipts, nil
}

// TestResultLabel verifies the §11 table glyphs for each outcome.
func TestResultLabel(t *testing.T) {
	cases := []struct {
		name string
		o    history.Outcome
		want string
	}{
		{"healthy", history.OutcomeHealthy, "✓ healthy"},
		{"failed", history.OutcomeFailed, "✗ failed"},
		{"rolled_back", history.OutcomeRolledBack, "↺ rolled_back"},
		{"unknown", history.Outcome("weird"), "weird"},
	}
	for _, c := range cases {
		if got := resultLabel(c.o); got != c.want {
			t.Errorf("resultLabel(%q) = %q, want %q", c.o, got, c.want)
		}
	}
}

// TestJoinChanges verifies the CHANGES column joins sorted services and uses
// a dash for an empty list.
func TestJoinChanges(t *testing.T) {
	cases := []struct {
		name    string
		changes []string
		want    string
	}{
		{"empty", nil, "-"},
		{"single", []string{"api"}, "api"},
		{"multiple", []string{"api", "worker"}, "api worker"},
	}
	for _, c := range cases {
		if got := joinChanges(c.changes); got != c.want {
			t.Errorf("joinChanges(%v) = %q, want %q", c.changes, got, c.want)
		}
	}
}

// TestCollectHistory_Order verifies the rows are newest-first to match the
// §11 example (most recent cycle at the top).
func TestCollectHistory_Order(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "old1", CompletedAt: time.Unix(1700000000, 0), Result: history.OutcomeHealthy, Changes: []string{"api"}},
		{Commit: "new1", CompletedAt: time.Unix(1700000100, 0), Result: history.OutcomeFailed, Changes: []string{"worker"}},
	}}
	rows, err := collectHistory(context.Background(), store)
	if err != nil {
		t.Fatalf("collectHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].commit != "new1" {
		t.Errorf("rows[0].commit = %q, want newest first (new1)", rows[0].commit)
	}
	if rows[1].commit != "old1" {
		t.Errorf("rows[1].commit = %q, want oldest last (old1)", rows[1].commit)
	}
}

// TestCollectHistory_SameMinutePreservesAppendOrder verifies that two
// deployments in the same minute keep their chronological (append) order
// instead of being reordered by a non-stable sort on the truncated HH:MM
// column. A failed cycle and its rollback often land in the same minute.
func TestCollectHistory_SameMinutePreservesAppendOrder(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{
		{Commit: "fail", CompletedAt: time.Unix(1700000000, 0), Result: history.OutcomeFailed, Changes: []string{"api"}},
		{Commit: "back", CompletedAt: time.Unix(1700000001, 0), Result: history.OutcomeRolledBack, Changes: []string{"api"}},
	}}
	rows, err := collectHistory(context.Background(), store)
	if err != nil {
		t.Fatalf("collectHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Newest first: the rollback (appended second) must be at the top.
	if rows[0].commit != "back" {
		t.Errorf("rows[0].commit = %q, want back (rollback, appended last)", rows[0].commit)
	}
	if rows[1].commit != "fail" {
		t.Errorf("rows[1].commit = %q, want fail (failed, appended first)", rows[1].commit)
	}
	if rows[0].time != rows[1].time {
		t.Errorf("same-minute rows have different time columns: %q vs %q", rows[0].time, rows[1].time)
	}
}

// TestCollectHistory_CrossMidnight verifies that an earlier-in-the-night
// deploy sorts below a later-in-the-night deploy even though a naive HH:MM
// string sort would place a 00:10 (next morning) row above a 23:50 row. The
// 00:10 receipt is appended first (happened earlier), the 23:50 receipt
// second (happened later that night), so 23:50 must appear first (newest).
func TestCollectHistory_CrossMidnight(t *testing.T) {
	day := time.Unix(1700000000, 0).UTC().Truncate(24 * time.Hour)
	early := day.Add(10 * time.Minute)             // 00:10 that day (earlier)
	late := day.Add(23*time.Hour + 50*time.Minute) // 23:50 that day (later)
	store := &memStore{receipts: []history.Receipt{
		{Commit: "early", CompletedAt: early, Result: history.OutcomeHealthy, Changes: []string{"api"}},
		{Commit: "late", CompletedAt: late, Result: history.OutcomeHealthy, Changes: []string{"api"}},
	}}
	rows, err := collectHistory(context.Background(), store)
	if err != nil {
		t.Fatalf("collectHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Append order is chronological: "late" (23:50) appended after "early"
	// (00:10), so it must be first (newest). A HH:MM string sort would have
	// put "00:10" first because "00:10" > "23:50" lexicographically.
	if rows[0].commit != "late" {
		t.Errorf("rows[0].commit = %q, want late (appended after early)", rows[0].commit)
	}
}

// TestCollectHistory_ResultAndChanges verifies the result glyph and changes
// column are derived from the receipt.
func TestCollectHistory_ResultAndChanges(t *testing.T) {
	store := &memStore{receipts: []history.Receipt{{
		Commit:      "abc1234def",
		CompletedAt: time.Unix(1700000000, 0),
		Result:      history.OutcomeRolledBack,
		Changes:     []string{"api", "worker"},
	}}}
	rows, err := collectHistory(context.Background(), store)
	if err != nil {
		t.Fatalf("collectHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].commit != "abc1234" {
		t.Errorf("commit = %q, want 7-char prefix", rows[0].commit)
	}
	if rows[0].result != "↺ rolled_back" {
		t.Errorf("result = %q, want ↺ rolled_back", rows[0].result)
	}
	if rows[0].changes != "api worker" {
		t.Errorf("changes = %q, want api worker", rows[0].changes)
	}
}

// TestCollectHistory_NilStore verifies a nil store yields no rows and no
// error, so a project without a journal is safe.
func TestCollectHistory_NilStore(t *testing.T) {
	rows, err := collectHistory(context.Background(), nil)
	if err != nil {
		t.Fatalf("collectHistory(nil): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
}

// TestCollectHistory_StoreError verifies a store read error propagates.
func TestCollectHistory_StoreError(t *testing.T) {
	_, err := collectHistory(context.Background(), errStore{})
	if err == nil {
		t.Fatal("collectHistory(errStore): want error, got nil")
	}
}

// TestWriteHistory_Format verifies the rendered output matches the §11 table
// shape with the header and aligned columns.
func TestWriteHistory_Format(t *testing.T) {
	rows := []historyRow{
		{time: "18:42", commit: "d71b2e4", result: "✓ healthy", changes: "api"},
		{time: "17:13", commit: "a01fd92", result: "✗ failed", changes: "worker"},
	}
	var buf bytes.Buffer
	writeHistory(&buf, rows)
	out := buf.String()
	for _, want := range []string{
		"TIME                 COMMIT     RESULT         CHANGES\n",
		"18:42                d71b2e4    ✓ healthy      api\n",
		"17:13                a01fd92    ✗ failed       worker\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// TestWriteHistory_Empty verifies the header is printed even with no rows.
func TestWriteHistory_Empty(t *testing.T) {
	var buf bytes.Buffer
	writeHistory(&buf, nil)
	if !strings.Contains(buf.String(), "TIME") {
		t.Errorf("empty history missing header; got:\n%s", buf.String())
	}
}

// errStore is defined in diff_test.go and reused here; it fails every List
// call so collectHistory error propagation can be exercised.
var _ history.Store = errStore{}
