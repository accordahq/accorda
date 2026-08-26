package state

import "strings"

// StringPorts converts a service's normalized ports to their canonical
// short-form strings (docs/ACCORDA.md §8). It is the inverse of the Compose
// parser's port normalization, kept in the state package so the canonical
// rendering of the target-agnostic Port value type lives with the type it
// renders rather than in a concrete target driver. The Compose writer and
// the `accorda diff` command both use it so their port rendering agrees.
func StringPorts(ports []Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, PortString(p))
	}
	return out
}

// StringVolumes converts a service's normalized volumes to their canonical
// short-form strings (docs/ACCORDA.md §8). It is the inverse of the Compose
// parser's volume normalization, kept in the state package for the same
// reason as StringPorts.
func StringVolumes(volumes []Volume) []string {
	out := make([]string, 0, len(volumes))
	for _, v := range volumes {
		out = append(out, VolumeString(v))
	}
	return out
}

// PortString renders a normalized port mapping in Compose short form:
// [ip:]host:container[/protocol]. The protocol suffix is omitted for the
// default "tcp".
func PortString(p Port) string {
	var b strings.Builder
	if p.HostIP != "" {
		b.WriteString(p.HostIP)
		b.WriteByte(':')
	}
	if p.Host != "" {
		b.WriteString(p.Host)
	}
	if p.Container != "" {
		if b.Len() > 0 {
			b.WriteByte(':')
		}
		b.WriteString(p.Container)
	}
	if p.Protocol != "" && p.Protocol != "tcp" {
		b.WriteByte('/')
		b.WriteString(p.Protocol)
	}
	return b.String()
}

// VolumeString renders a normalized volume mount in Compose short form
// (docs/ACCORDA.md §8). An anonymous volume (no source) renders as just the
// in-container target path. A read-only mount appends ":ro".
func VolumeString(v Volume) string {
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
