package reconcile

import (
	"context"
	"fmt"
	"time"
)

// ResultHandler observes the outcome of each reconciliation cycle run by
// Run. It is called synchronously, so handlers should return promptly.
type ResultHandler func(*Result)

// Run continuously reconciles until ctx is cancelled. It runs one cycle
// immediately, then starts another cycle whenever interval elapses. Failed
// cycles are reported to handle and do not stop polling, allowing transient
// source or target failures to recover on a later cycle.
//
// Reconcile fetches the tracked branch on every cycle. An unchanged HEAD
// skips planning and target mutation while still checking runtime state for
// drift; a new HEAD is planned and deployed through the normal lifecycle.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration, handle ResultHandler) error {
	if interval <= 0 {
		return fmt.Errorf("reconcile: polling interval must be positive: %s", interval)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		result := r.Reconcile(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if handle != nil {
			handle(result)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}
