package docker

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

// PullAction is a pull action selected by an image pull policy. It mirrors
// plan.Action for the pull subset a target driver prepends to its plan
// (docs/ACCORDA.md §9).
type PullAction struct {
	Service string
	Image   string
}

// SelectPulls returns the pull actions to prepend to a plan according to the
// pull policy (docs/ACCORDA.md §9). The policy selects which of the desired
// services' images must be fetched before deployment:
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
// know which services changed. client is used only by the "missing" policy to
// enumerate local images.
func SelectPulls(ctx context.Context, client Client, policy string, desired *state.DesiredState, drift []plan.Action) ([]PullAction, error) {
	switch policy {
	case config.PullNever:
		return nil, nil
	case config.PullAlways:
		return PullAll(desired), nil
	case config.PullMissing:
		local, err := LocalImages(ctx, client)
		if err != nil {
			return nil, err
		}
		return PullMissing(desired, local), nil
	case config.PullChanged:
		return PullChanged(desired, drift), nil
	default:
		return nil, fmt.Errorf("docker: unknown pull policy %q", policy)
	}
}

// PullAll returns a pull action for every desired service, ordered by service
// name (docs/ACCORDA.md §9 "always").
func PullAll(desired *state.DesiredState) []PullAction {
	var actions []PullAction
	for _, name := range SortedServiceNames(desired.Services) {
		actions = append(actions, PullAction{Service: name, Image: desired.Services[name].Image})
	}
	return actions
}

// PullChanged returns a pull action for each service that changed (created or
// recreated), ordered by service name (docs/ACCORDA.md §9 "changed"). A
// stopped service with an unchanged image (a Start action) already has its
// image locally, so it is not pulled.
func PullChanged(desired *state.DesiredState, drift []plan.Action) []PullAction {
	changed := make(map[string]bool)
	for _, a := range drift {
		if a.Kind == plan.ActionCreate || a.Kind == plan.ActionRecreate {
			changed[a.Service] = true
		}
	}
	var actions []PullAction
	for _, name := range SortedServiceNames(desired.Services) {
		if changed[name] {
			actions = append(actions, PullAction{Service: name, Image: desired.Services[name].Image})
		}
	}
	return actions
}

// PullMissing returns a pull action for each desired service whose image is
// not present in local, ordered by service name (docs/ACCORDA.md §9
// "missing"). local maps image references (repo tags and repo digests) that
// are already available on the engine.
func PullMissing(desired *state.DesiredState, local map[string]bool) []PullAction {
	var actions []PullAction
	for _, name := range SortedServiceNames(desired.Services) {
		img := desired.Services[name].Image
		if !local[img] {
			actions = append(actions, PullAction{Service: name, Image: img})
		}
	}
	return actions
}

// LocalImages returns the set of image references currently available on the
// Docker engine, read via the client seam. It is used by the "missing" pull
// policy to decide which images still need pulling.
//
// Both repo tags and repo digests are indexed. A digest-pinned image
// (e.g. "ghcr.io/acme/api@sha256:91a...") is pulled by digest, so Docker
// populates RepoDigests but leaves RepoTags empty; indexing only tags would
// make such images look perpetually missing and re-pull them on every
// deployment (docs/ACCORDA.md §7 emphasizes recording digests).
func LocalImages(ctx context.Context, client Client) (map[string]bool, error) {
	if client == nil {
		return nil, errors.New("docker: client is nil")
	}
	summaries, err := client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: list images: %w", err)
	}
	local := make(map[string]bool)
	for _, s := range summaries {
		for _, tag := range s.RepoTags {
			local[tag] = true
		}
		for _, digest := range s.RepoDigests {
			local[digest] = true
		}
	}
	return local, nil
}

// SortedServiceNames returns the service names of services in sorted order so
// pull actions are deterministic regardless of Go map iteration order
// (docs/DECISIONS.md #12).
func SortedServiceNames(services map[string]state.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
