package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"accorda/internal/core/state"
)

// writeComposeServices materializes a set of Accorda services into the
// project's Compose file on disk. It is used by ApplyDesired to make the
// on-disk Compose file reflect a rollback target before `docker compose up
// -d` runs, because the runner resolves services against the file and would
// otherwise re-apply the image currently on disk rather than the restored
// one.
//
// Only the reconciliation-relevant subset of the service model is written
// back (image, command, environment, ports, volumes, networks, labels,
// healthcheck, depends_on), mirroring the subset the loader normalizes.
func writeComposeServices(path string, services map[string]state.Service) error {
	doc := map[string]any{
		"services": composeServices(services),
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("compose: encode rollback services: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("compose: close encoder: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("compose: create dir: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("compose: write %q: %w", path, err)
	}
	return nil
}

// composeServices converts Accorda services into the YAML-marshalable map
// form. Service names are sorted so the output is deterministic
// (docs/DECISIONS.md #12).
func composeServices(services map[string]state.Service) map[string]map[string]any {
	out := make(map[string]map[string]any, len(services))
	for _, name := range sortedServiceNames(services) {
		out[name] = composeService(services[name])
	}
	return out
}

// composeService converts a single Accorda service into its YAML form.
func composeService(s state.Service) map[string]any {
	m := map[string]any{}
	if s.Image != "" {
		m["image"] = s.Image
	}
	if len(s.Command) > 0 {
		m["command"] = s.Command
	}
	if len(s.Env) > 0 {
		m["environment"] = s.Env
	}
	if len(s.Ports) > 0 {
		m["ports"] = StringPorts(s.Ports)
	}
	if len(s.Volumes) > 0 {
		m["volumes"] = StringVolumes(s.Volumes)
	}
	if len(s.Networks) > 0 {
		m["networks"] = s.Networks
	}
	if len(s.Labels) > 0 {
		m["labels"] = s.Labels
	}
	if hc := composeHealthcheck(s.Healthcheck); hc != nil {
		m["healthcheck"] = hc
	}
	if len(s.DependsOn) > 0 {
		// DependsOn is already sorted by the state model for deterministic
		// comparison and hashing (docs/DECISIONS.md #12).
		m["depends_on"] = s.DependsOn
	}
	return m
}

// StringPorts converts a service's normalized ports to Compose short-form
// strings. It is exported so the `accorda diff` CLI command renders ports
// identically to the Compose writer, keeping the canonical form in one place
// (docs/DECISIONS.md #12, #13).
func StringPorts(ports []state.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, portString(p))
	}
	return out
}

// StringVolumes converts a service's normalized volumes to Compose short-form
// strings. It is exported so the `accorda diff` CLI command renders volumes
// identically to the Compose writer, keeping the canonical form in one place
// (docs/DECISIONS.md #12, #13).
func StringVolumes(volumes []state.Volume) []string {
	out := make([]string, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, volumeString(v))
	}
	return out
}

// composeHealthcheck converts a normalized healthcheck into its YAML form,
// returning nil when the healthcheck is unset (no test and not disabled).
func composeHealthcheck(h state.Healthcheck) map[string]any {
	if h.Test == nil && !h.Disable {
		return nil
	}
	hc := map[string]any{}
	if len(h.Test) > 0 {
		hc["test"] = h.Test
	}
	if h.Interval > 0 {
		hc["interval"] = h.Interval.String()
	}
	if h.Timeout > 0 {
		hc["timeout"] = h.Timeout.String()
	}
	if h.Retries > 0 {
		hc["retries"] = h.Retries
	}
	if h.StartPeriod > 0 {
		hc["start_period"] = h.StartPeriod.String()
	}
	if h.Disable {
		hc["disable"] = true
	}
	return hc
}

// portString renders a normalized port mapping in Compose short form
// (docs/ACCORDA.md §8). Only the fields the loader preserves are emitted.
func portString(p state.Port) string {
	var b []byte
	if p.HostIP != "" {
		b = append(b, p.HostIP...)
		b = append(b, ':')
	}
	if p.Host != "" {
		b = append(b, p.Host...)
	}
	if p.Container != "" {
		if len(b) > 0 {
			b = append(b, ':')
		}
		b = append(b, p.Container...)
	}
	if p.Protocol != "" && p.Protocol != "tcp" {
		b = append(b, '/')
		b = append(b, p.Protocol...)
	}
	return string(b)
}

// volumeString renders a normalized volume mount in Compose short form
// (docs/ACCORDA.md §8). An anonymous volume (no source) renders as just the
// in-container target path.
func volumeString(v state.Volume) string {
	out := v.Source
	if v.Target != "" {
		if out != "" {
			out += ":"
		}
		out += v.Target
	}
	if v.ReadOnly {
		out += ":ro"
	}
	return out
}
