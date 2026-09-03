package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"

	"accorda/internal/core/state"
)

// degradedStatus is the runtime status reported for a service whose replicas
// disagree (for example one running and one stopped). It signals drift that
// a single last-wins entry would otherwise hide (docs/ACCORDA.md §5.3).
const degradedStatus = "degraded"

// RuntimeService maps a Docker container inspect response to Accorda's
// RuntimeService. The Status field uses Docker's ContainerState string
// ("running", "exited", ...); the Health field uses the healthcheck status
// ("healthy", "unhealthy", "starting") or "" when there is no healthcheck.
//
// The Image field is set from the image reference the operator passed to the
// engine (Config.Image), not the resolved image ID (ContainerJSONBase.Image).
// The desired state carries image references (e.g. "busybox:1.36"), so the
// runtime image must be a reference for the desired-vs-runtime comparison to
// agree; comparing against the image ID would always report drift. Config.Image
// is preferred when present; ContainerJSONBase.Image is used only as a
// fallback for engine responses that omit Config (docs/ACCORDA.md §5.3).
func RuntimeService(c container.InspectResponse) state.RuntimeService {
	svc := state.RuntimeService{}
	if c.ContainerJSONBase != nil {
		svc.Image = ImageReference(c)
		if c.ContainerJSONBase.State != nil {
			svc.Status = string(c.ContainerJSONBase.State.Status)
			svc.ExitCode = c.ContainerJSONBase.State.ExitCode
			if c.ContainerJSONBase.State.Health != nil {
				h := string(c.ContainerJSONBase.State.Health.Status)
				// "none" means no healthcheck; surface it as empty so callers
				// can treat "" as "no health information".
				if h != string(container.NoHealthcheck) {
					svc.Health = h
				}
			}
		}
	}
	return svc
}

// ImageReference returns the image reference of the container for comparison
// against desired state. It prefers Config.Image (the reference the operator
// passed, e.g. "busybox:1.36") and falls back to ContainerJSONBase.Image (the
// resolved image ID, e.g. "sha256:91a...") only when Config is absent. The
// desired state models image references (docs/ACCORDA.md §8), so comparing
// against a reference keeps desired and runtime comparable.
func ImageReference(c container.InspectResponse) string {
	if c.Config != nil && c.Config.Image != "" {
		return c.Config.Image
	}
	if c.ContainerJSONBase != nil {
		return c.ContainerJSONBase.Image
	}
	return ""
}

// MergeRuntime combines two RuntimeService values for the same service name
// (multiple replicas). When the replicas agree, the merged value is that
// shared state; when they disagree on status or health, the merged value
// reports the degraded status so per-replica drift is surfaced rather than
// silently overwritten.
func MergeRuntime(a, b state.RuntimeService) state.RuntimeService {
	if a.Status != b.Status || a.Health != b.Health || a.ExitCode != b.ExitCode {
		return state.RuntimeService{Status: degradedStatus, Health: "", Image: a.Image}
	}
	return a
}

// DegradedStatus returns the runtime status value reported for a service
// whose replicas disagree.
func DegradedStatus() string { return degradedStatus }

// ResolveDigests fills in the Digest field of each runtime service by
// inspecting the service's image on the engine and reading its manifest
// digest (RepoDigests). It is best-effort: an image that cannot be inspected
// (for example a locally built image with no registry manifest) keeps an
// empty digest. Results are cached per image reference so a multi-replica
// service or a shared image is inspected only once.
func ResolveDigests(ctx context.Context, client Client, services map[string]state.RuntimeService) {
	cache := make(map[string]string)
	for name, svc := range services {
		if svc.Image == "" {
			continue
		}
		digest, ok := cache[svc.Image]
		if !ok {
			digest = ImageDigest(ctx, client, svc.Image)
			cache[svc.Image] = digest
		}
		svc.Digest = digest
		services[name] = svc
	}
}

// ImageDigest returns the manifest digest of the given image reference, or ""
// when it cannot be resolved. The digest is read from the image's
// RepoDigests, which Docker populates when the image was pulled from (or
// pushed to) a registry; a locally built image has no manifest digest and
// yields "".
func ImageDigest(ctx context.Context, client Client, ref string) string {
	if client == nil {
		return ""
	}
	inspected, err := client.ImageInspect(ctx, ref)
	if err != nil {
		return ""
	}
	if len(inspected.RepoDigests) == 0 {
		return ""
	}
	return inspected.RepoDigests[0]
}
