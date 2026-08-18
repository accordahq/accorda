package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	composeloader "github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"accorda/internal/core/state"
)

// LoadFile reads and validates a Docker Compose file at path, returning the
// normalized services keyed by service name. The path is resolved relative
// to the caller (an absolute path or one relative to the working directory).
//
// LoadFile is the entry point for the Compose target's Validate phase
// (docs/ACCORDA.md §6): it loads the desired Compose file, normalizes it
// into Accorda's service model, and rejects files that declare a service
// without the required fields before any deployment work begins.
func LoadFile(path string) (map[string]state.Service, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compose: read %q: %w", path, err)
	}
	services, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("compose: %s: %w", filepath.Base(path), err)
	}
	return services, nil
}

// Parse decodes a Docker Compose document from raw YAML bytes and normalizes
// it into Accorda's service model. It uses the compose-go loader
// (github.com/compose-spec/compose-go/v2), which handles the full Compose
// schema including interpolation, extends, profiles, short and long forms
// for all fields. Accorda's model is a subset of the Compose schema: image,
// command, environment, ports, volumes, networks, labels, healthcheck, and
// depends_on. Validation enforces that every service has an image.
//
// Parse is the pure entry point for in-memory bytes; LoadFile wraps it for
// file-path-based loading. The compose-go loader handles YAML parsing,
// interpolation, extends, and normalization so Accorda does not maintain
// its own parser.
func Parse(data []byte) (map[string]state.Service, error) {
	return ParseWithContext(context.Background(), data)
}

// ParseWithContext is like Parse but accepts a context for cancellation.
func ParseWithContext(ctx context.Context, data []byte) (map[string]state.Service, error) {
	project, err := composeloader.LoadWithContext(ctx, types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{{Content: data}},
		Environment: types.Mapping{},
	}, func(o *composeloader.Options) {
		o.SkipInterpolation = true
		o.SkipValidation = true
	})
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	services := make(map[string]state.Service, len(project.Services))
	for name, sc := range project.Services {
		svc, err := normalizeService(name, sc)
		if err != nil {
			return nil, err
		}
		services[name] = svc
	}
	return services, nil
}

// normalizeService converts a compose-go ServiceConfig into Accorda's
// state.Service, validating required fields. Fields Accorda does not model
// (build, deploy, cpus, etc.) are ignored; the spec calls for Accorda to
// reason about the reconciliation-relevant subset only.
func normalizeService(name string, sc types.ServiceConfig) (state.Service, error) {
	svc := state.Service{
		Image:       sc.Image,
		Command:     normalizeCommand(sc.Command),
		Env:         normalizeEnv(sc.Environment),
		Ports:       normalizePorts(sc.Ports),
		Volumes:     normalizeVolumes(sc.Volumes),
		Networks:    normalizeNetworks(sc.Networks),
		Labels:      normalizeLabels(sc.Labels),
		Healthcheck: normalizeHealthcheck(sc.HealthCheck),
		DependsOn:   normalizeDependsOn(sc.DependsOn),
	}
	if err := validateService(name, svc); err != nil {
		return svc, err
	}
	return svc, nil
}

// validateService enforces the required-field rules the spec calls out for a
// normalized Compose service (docs/ACCORDA.md §8). A service must declare an
// image; Accorda's service model is image-centric, so a build-only service
// fails validation at load time.
func validateService(name string, svc state.Service) error {
	if svc.Image == "" {
		return fmt.Errorf("service %q: image is required", name)
	}
	for i, p := range svc.Ports {
		if p.Container == "" {
			return fmt.Errorf("service %q: ports[%d]: container port is required", name, i)
		}
	}
	for i, v := range svc.Volumes {
		if v.Target == "" {
			return fmt.Errorf("service %q: volumes[%d]: target is required", name, i)
		}
	}
	return nil
}

// normalizeCommand converts the compose-go ShellCommand ([]string) to
// Accorda's []string.
func normalizeCommand(cmd types.ShellCommand) []string {
	if len(cmd) == 0 {
		return nil
	}
	return []string(cmd)
}

// normalizeEnv converts the compose-go MappingWithEquals to a plain
// map[string]string. Nil values (unset env vars from the Compose list form)
// are preserved as empty strings.
func normalizeEnv(env types.MappingWithEquals) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if v == nil {
			out[k] = ""
		} else {
			out[k] = *v
		}
	}
	return out
}

// normalizePorts converts compose-go ServicePortConfig slices to Accorda's
// Port. The compose-go loader resolves short and long forms and provides
// Target as uint32; Accorda stores ports as strings to preserve ranges.
func normalizePorts(ports []types.ServicePortConfig) []state.Port {
	if len(ports) == 0 {
		return nil
	}
	out := make([]state.Port, 0, len(ports))
	for _, p := range ports {
		out = append(out, state.Port{
			HostIP:    p.HostIP,
			Host:      p.Published,
			Container: strconv.FormatUint(uint64(p.Target), 10),
			Protocol:  defaultIfEmpty(p.Protocol, "tcp"),
		})
	}
	return out
}

// normalizeVolumes converts compose-go ServiceVolumeConfig slices to Accorda's
// Volume.
func normalizeVolumes(vols []types.ServiceVolumeConfig) []state.Volume {
	if len(vols) == 0 {
		return nil
	}
	out := make([]state.Volume, 0, len(vols))
	for _, v := range vols {
		out = append(out, state.Volume{
			Type:     defaultIfEmpty(v.Type, "volume"),
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		})
	}
	return out
}

// normalizeNetworks converts the compose-go networks map to a slice of
// network names.
func normalizeNetworks(networks map[string]*types.ServiceNetworkConfig) []string {
	if len(networks) == 0 {
		return nil
	}
	out := make([]string, 0, len(networks))
	for name := range networks {
		out = append(out, name)
	}
	return out
}

// normalizeLabels converts the compose-go Labels map to a plain
// map[string]string.
func normalizeLabels(labels types.Labels) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// normalizeHealthcheck converts the compose-go HealthCheckConfig to Accorda's
// Healthcheck.
func normalizeHealthcheck(hc *types.HealthCheckConfig) state.Healthcheck {
	if hc == nil {
		return state.Healthcheck{}
	}
	return state.Healthcheck{
		Test:        hc.Test,
		Interval:    durationFromPtr(hc.Interval),
		Timeout:     durationFromPtr(hc.Timeout),
		Retries:     retriesFromPtr(hc.Retries),
		StartPeriod: durationFromPtr(hc.StartPeriod),
		Disable:     hc.Disable,
	}
}

// normalizeDependsOn converts the compose-go DependsOnConfig map to a slice
// of service names.
func normalizeDependsOn(deps types.DependsOnConfig) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, 0, len(deps))
	for name := range deps {
		out = append(out, name)
	}
	return out
}

// durationFromPtr dereferences a compose-go *Duration into a time.Duration,
// returning 0 for nil.
func durationFromPtr(d *types.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(*d)
}

// retriesFromPtr dereferences a *uint64 into an int, returning 0 for nil.
func retriesFromPtr(r *uint64) int {
	if r == nil {
		return 0
	}
	return int(*r)
}

// defaultIfEmpty returns val if non-empty, otherwise def.
func defaultIfEmpty(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
