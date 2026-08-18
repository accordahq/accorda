package compose

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/docker/docker/api/types/image"

	"accorda/internal/config"
	"accorda/internal/core/plan"
	"accorda/internal/core/state"
)

// selectPulls returns the pull actions to prepend to a plan according to the
// target's image pull policy (docs/ACCORDA.md §9). The policy selects which
// of the desired services' images must be fetched before deployment:
//
//   - changed: pull only the images of services that changed (created or
//     recreated), so unchanged services are left untouched.
//   - missing: pull only images that are not already available locally.
//   - always: pull every desired service's image on every deployment.
//   - never: pull nothing; the target relies on images already being present.
//
// The returned actions are ordered by service name so the plan stays
// deterministic (docs/DECISIONS.md #12). drift is the desired-vs-deployed
// diff already computed by plan.DriftActions; the "changed" policy uses it to
// know which services changed.
func (t *Target) selectPulls(ctx context.Context, desired *state.DesiredState, drift []plan.Action) ([]plan.Action, error) {
	switch t.pullPolicy {
	case config.PullNever:
		return nil, nil
	case config.PullAlways:
		return pullAll(desired), nil
	case config.PullMissing:
		local, err := t.localImages(ctx)
		if err != nil {
			return nil, err
		}
		return pullMissing(desired, local), nil
	case config.PullChanged:
		return pullChanged(desired, drift), nil
	default:
		return nil, fmt.Errorf("compose target: unknown pull policy %q", t.pullPolicy)
	}
}

// pullAll returns a pull action for every desired service, ordered by service
// name (docs/ACCORDA.md §9 "always").
func pullAll(desired *state.DesiredState) []plan.Action {
	var actions []plan.Action
	for _, name := range sortedServiceNames(desired.Services) {
		actions = append(actions, plan.Action{Kind: plan.ActionPull, Service: name, Image: desired.Services[name].Image})
	}
	return actions
}

// pullChanged returns a pull action for each service that changed (created or
// recreated), ordered by service name (docs/ACCORDA.md §9 "changed"). A
// stopped service with an unchanged image (a Start action) already has its
// image locally, so it is not pulled.
func pullChanged(desired *state.DesiredState, drift []plan.Action) []plan.Action {
	changed := make(map[string]bool)
	for _, a := range drift {
		if a.Kind == plan.ActionCreate || a.Kind == plan.ActionRecreate {
			changed[a.Service] = true
		}
	}
	var actions []plan.Action
	for _, name := range sortedServiceNames(desired.Services) {
		if changed[name] {
			actions = append(actions, plan.Action{Kind: plan.ActionPull, Service: name, Image: desired.Services[name].Image})
		}
	}
	return actions
}

// pullMissing returns a pull action for each desired service whose image is
// not present in local, ordered by service name (docs/ACCORDA.md §9
// "missing"). local maps image references (repo tags) that are already
// available on the engine.
func pullMissing(desired *state.DesiredState, local map[string]bool) []plan.Action {
	var actions []plan.Action
	for _, name := range sortedServiceNames(desired.Services) {
		img := desired.Services[name].Image
		if !local[img] {
			actions = append(actions, plan.Action{Kind: plan.ActionPull, Service: name, Image: img})
		}
	}
	return actions
}

// localImages returns the set of image references (repo tags) currently
// available on the Docker engine, read via the dockerClient seam. It is used
// by the "missing" pull policy to decide which images still need pulling.
func (t *Target) localImages(ctx context.Context) (map[string]bool, error) {
	if t.docker == nil {
		return nil, errors.New("compose target: docker client is nil")
	}
	summaries, err := t.docker.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("compose target: list images: %w", err)
	}
	local := make(map[string]bool)
	for _, s := range summaries {
		for _, tag := range s.RepoTags {
			local[tag] = true
		}
	}
	return local, nil
}

// sortedServiceNames returns the service names of services in sorted order so
// pull actions are deterministic regardless of Go map iteration order
// (docs/DECISIONS.md #12).
func sortedServiceNames(services map[string]state.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
