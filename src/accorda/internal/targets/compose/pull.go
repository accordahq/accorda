package compose

import (
	"context"
	"fmt"

	"accorda/internal/core/plan"
	"accorda/internal/core/state"
	shareddocker "accorda/internal/docker"
)

// selectPulls returns the pull actions to prepend to a plan according to the
// target's image pull policy (docs/ACCORDA.md §9). It delegates the
// policy-specific selection to the shared Docker operations layer and
// converts the shared PullAction values into plan.Actions.
//
// The returned actions are ordered by service name so the plan stays
// deterministic (docs/DECISIONS.md #12). drift is the desired-vs-deployed
// diff already computed by plan.DriftActions; the "changed" policy uses it to
// know which services changed.
func (t *Target) selectPulls(ctx context.Context, desired *state.DesiredState, drift []plan.Action) ([]plan.Action, error) {
	pulls, err := shareddocker.SelectPulls(ctx, t.docker, t.pullPolicy, desired, drift)
	if err != nil {
		return nil, fmt.Errorf("compose target: %w", err)
	}
	return toPullActions(pulls), nil
}

// toPullActions converts the shared PullAction values into plan.Action
// values, preserving the deterministic service-name order
// (docs/DECISIONS.md #12).
func toPullActions(pulls []shareddocker.PullAction) []plan.Action {
	actions := make([]plan.Action, 0, len(pulls))
	for _, p := range pulls {
		actions = append(actions, plan.Action{Kind: plan.ActionPull, Service: p.Service, Image: p.Image})
	}
	return actions
}
