package compose

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"accorda/internal/core/state"

	"gopkg.in/yaml.v3"
)

// yamlNode is an alias for the underlying YAML node type so the parser code
// reads as Compose-specific rather than YAML-internal. It carries no extra
// behavior; it is only a readability indirection.
type yamlNode = yaml.Node

// yamlMappingNode and yamlSequenceNode are the yaml.Node kinds used here.
// They are redeclared as untyped constants so the parser does not import the
// yaml package's constants by name elsewhere; the names mirror yaml's own.
const (
	yamlScalarNode   = yaml.ScalarNode
	yamlMappingNode  = yaml.MappingNode
	yamlSequenceNode = yaml.SequenceNode
	yamlDocumentNode = yaml.DocumentNode
)

// decode parses data into an ordered map of top-level YAML nodes. Keys are
// the top-level mapping keys; values are the raw nodes so the services map
// can be normalized field-by-field. Unknown top-level keys are not rejected
// here so that a Compose file with a top-level `volumes:` or `networks:`
// declaration does not fail; only the `services:` section is consumed.
func decode(data []byte) (map[string]*yamlNode, error) {
	var root yamlNode
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if root.Kind == 0 {
		// Empty document.
		return nil, nil
	}
	// yaml.Unmarshal into a *yaml.Node yields a DocumentNode wrapping the
	// actual content; unwrap it so the mapping check below sees the map.
	mapping := &root
	if root.Kind == yamlDocumentNode {
		if len(root.Content) == 0 {
			return nil, nil
		}
		mapping = root.Content[0]
	}
	if mapping.Kind != yamlMappingNode {
		return nil, fmt.Errorf("expected a mapping at the document root, got %s", nodeKindName(mapping))
	}
	out := make(map[string]*yamlNode, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		val := mapping.Content[i+1]
		out[key.Value] = val
	}
	return out, nil
}

// nodeKindName returns a human-readable name for a node's kind, for error
// messages.
func nodeKindName(n *yamlNode) string {
	switch n.Kind {
	case yamlScalarNode:
		return "scalar"
	case yamlMappingNode:
		return "mapping"
	case yamlSequenceNode:
		return "sequence"
	default:
		return fmt.Sprintf("kind(%d)", n.Kind)
	}
}

// decodeScalar returns the scalar value of a node as a string. A null node
// yields the empty string. Non-scalar nodes yield an error.
func decodeScalar(n *yamlNode) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Value)
}

// decodeStringOrList accepts either a scalar (shell form) or a sequence
// (exec form) and returns the normalized list. A scalar is returned as a
// single-element slice; a sequence is returned verbatim. A null or empty
// node yields nil.
func decodeStringOrList(n *yamlNode) []string {
	if n == nil || n.Kind == 0 {
		return nil
	}
	switch n.Kind {
	case yamlScalarNode:
		if n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" {
			return nil
		}
		return []string{strings.TrimSpace(n.Value)}
	case yamlSequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			out = append(out, strings.TrimSpace(item.Value))
		}
		return out
	default:
		return nil
	}
}

// decodeEnvironment decodes a Compose environment field into env. Compose
// accepts a mapping (KEY: value), a list of "KEY=VALUE"/"KEY" entries, or a
// named file reference (which Accorda does not resolve at parse time).
func decodeEnvironment(n *yamlNode, env map[string]string) error {
	if n == nil || n.Kind == 0 {
		return nil
	}
	switch n.Kind {
	case yamlScalarNode:
		// A scalar env value is treated as a single KEY=VALUE entry,
		// mirroring the short-form accepted by some Compose versions.
		setEnvEntry(env, n.Value)
		return nil
	case yamlMappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1].Value
			if n.Content[i+1].Tag == "!!null" {
				env[key] = ""
				continue
			}
			env[key] = unquote(val)
		}
		return nil
	case yamlSequenceNode:
		for _, item := range n.Content {
			setEnvEntry(env, item.Value)
		}
		return nil
	default:
		return errors.New("must be a mapping, a list, or a scalar")
	}
}

// setEnvEntry records a "KEY=VALUE" or "KEY" entry into env. A bare KEY maps
// to the empty string, matching Compose's environment list form.
func setEnvEntry(env map[string]string, raw string) {
	raw = unquote(strings.TrimSpace(raw))
	if raw == "" {
		return
	}
	if i := strings.Index(raw, "="); i >= 0 {
		env[raw[:i]] = raw[i+1:]
	} else {
		env[raw] = ""
	}
}

// decodePorts decodes Compose's short and long port forms.
//
// Short form examples:
//
//   - "8080:8080"
//   - "127.0.0.1:8080:8080"
//   - "8080"
//   - "8080-8085:8080-8085/tcp"
//
// Long form:
//
//   - target: 8080
//     published: 8080
//     host_ip: 127.0.0.1
//     protocol: tcp
func decodePorts(n *yamlNode) ([]state.Port, error) {
	if n == nil || n.Kind == 0 {
		return nil, nil
	}
	if n.Kind != yamlSequenceNode {
		return nil, errors.New("must be a sequence")
	}
	out := make([]state.Port, 0, len(n.Content))
	for _, item := range n.Content {
		switch item.Kind {
		case yamlScalarNode:
			out = append(out, parseShortPort(item.Value))
		case yamlMappingNode:
			p, err := parseLongPort(item)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		default:
			return nil, fmt.Errorf("port entry must be a scalar or mapping, got %s", nodeKindName(item))
		}
	}
	return out, nil
}

// parseShortPort parses a short-form port string. It tolerates the
// host_ip:host:container, host:container, and bare container forms, plus an
// optional /protocol suffix.
func parseShortPort(s string) state.Port {
	s = unquote(strings.TrimSpace(s))
	p := state.Port{Protocol: "tcp"}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		p.Protocol = s[i+1:]
		s = s[:i]
	}
	if s == "" {
		return p
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		p.Container = parts[0]
	case 2:
		p.Host = parts[0]
		p.Container = parts[1]
	case 3:
		p.HostIP = parts[0]
		p.Host = parts[1]
		p.Container = parts[2]
	}
	return p
}

// parseLongPort parses the long-form port mapping.
func parseLongPort(n *yamlNode) (state.Port, error) {
	p := state.Port{Protocol: "tcp"}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1].Value
		switch key {
		case "target":
			p.Container = val
		case "published":
			p.Host = val
		case "host_ip":
			p.HostIP = val
		case "protocol":
			if val != "" {
				p.Protocol = val
			}
		}
	}
	return p, nil
}

// decodeVolumes decodes Compose's short and long volume forms.
//
// Short form:
//
//   - /host/path:/container/path
//   - /host/path:/container/path:ro
//   - named_volume:/data
//   - /container/path
//
// Long form:
//
//   - type: bind
//     source: /host/path
//     target: /container/path
//     read_only: true
func decodeVolumes(n *yamlNode) ([]state.Volume, error) {
	if n == nil || n.Kind == 0 {
		return nil, nil
	}
	if n.Kind != yamlSequenceNode {
		return nil, errors.New("must be a sequence")
	}
	out := make([]state.Volume, 0, len(n.Content))
	for _, item := range n.Content {
		switch item.Kind {
		case yamlScalarNode:
			out = append(out, parseShortVolume(item.Value))
		case yamlMappingNode:
			v, err := parseLongVolume(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		default:
			return nil, fmt.Errorf("volume entry must be a scalar or mapping, got %s", nodeKindName(item))
		}
	}
	return out, nil
}

// parseShortVolume parses a short-form volume string.
func parseShortVolume(s string) state.Volume {
	s = unquote(strings.TrimSpace(s))
	v := state.Volume{Type: "volume"}
	if s == "" {
		return v
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		// Anonymous volume: just the container path.
		v.Target = parts[0]
		v.Type = "volume"
	case 2:
		v.Source = parts[0]
		v.Target = parts[1]
		v.Type = volumeTypeFor(parts[0])
	case 3:
		v.Source = parts[0]
		v.Target = parts[1]
		v.Type = volumeTypeFor(parts[0])
		v.ReadOnly = parts[2] == "ro"
	}
	return v
}

// parseLongVolume parses the long-form volume mapping.
func parseLongVolume(n *yamlNode) (state.Volume, error) {
	v := state.Volume{Type: "volume"}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1].Value
		switch key {
		case "type":
			if val != "" {
				v.Type = val
			}
		case "source":
			v.Source = val
		case "target":
			v.Target = val
		case "read_only":
			v.ReadOnly = val == "true"
		}
	}
	return v, nil
}

// volumeTypeFor infers the volume type from a short-form source. An absolute
// path is a bind mount; anything else is treated as a named volume.
func volumeTypeFor(source string) string {
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") {
		return "bind"
	}
	return "volume"
}

// decodeNetworks decodes the networks field. Compose accepts a list of
// network names or a mapping of network names to options; only the names are
// retained.
func decodeNetworks(n *yamlNode) ([]string, error) {
	if n == nil || n.Kind == 0 {
		return nil, nil
	}
	switch n.Kind {
	case yamlSequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			if name := strings.TrimSpace(item.Value); name != "" {
				out = append(out, name)
			}
		}
		return out, nil
	case yamlMappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out, nil
	default:
		return nil, errors.New("must be a sequence or mapping")
	}
}

// decodeMapping decodes a Compose mapping field (e.g. labels) into dst. A
// sequence of "KEY=VALUE" entries is also accepted for parity with Compose's
// label list form.
func decodeMapping(n *yamlNode, dst map[string]string) error {
	if n == nil || n.Kind == 0 {
		return nil
	}
	switch n.Kind {
	case yamlMappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1].Value
			if n.Content[i+1].Tag == "!!null" {
				dst[key] = ""
				continue
			}
			dst[key] = unquote(val)
		}
		return nil
	case yamlSequenceNode:
		for _, item := range n.Content {
			setEnvEntry(dst, item.Value)
		}
		return nil
	default:
		return errors.New("must be a mapping or sequence")
	}
}

// decodeHealthcheck decodes the healthcheck mapping.
func decodeHealthcheck(n *yamlNode) (state.Healthcheck, error) {
	hc := state.Healthcheck{}
	if n == nil || n.Kind == 0 {
		return hc, nil
	}
	if n.Kind != yamlMappingNode {
		return hc, errors.New("must be a mapping")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		switch key {
		case "test":
			hc.Test = decodeHealthcheckTest(val)
		case "interval":
			d, err := decodeDuration(val.Value)
			if err != nil {
				return hc, fmt.Errorf("interval: %w", err)
			}
			hc.Interval = d
		case "timeout":
			d, err := decodeDuration(val.Value)
			if err != nil {
				return hc, fmt.Errorf("timeout: %w", err)
			}
			hc.Timeout = d
		case "retries":
			hc.Retries = parseInt(val.Value)
		case "start_period":
			d, err := decodeDuration(val.Value)
			if err != nil {
				return hc, fmt.Errorf("start_period: %w", err)
			}
			hc.StartPeriod = d
		case "disable":
			hc.Disable = val.Value == "true"
		}
	}
	return hc, nil
}

// decodeHealthcheckTest normalizes the healthcheck test. A scalar is treated
// as a CMD-SHELL form: ["CMD-SHELL", "<string>"]. A list is stored verbatim.
func decodeHealthcheckTest(n *yamlNode) []string {
	if n == nil || n.Kind == 0 {
		return nil
	}
	switch n.Kind {
	case yamlScalarNode:
		if strings.TrimSpace(n.Value) == "" {
			return nil
		}
		return []string{"CMD-SHELL", strings.TrimSpace(n.Value)}
	case yamlSequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			out = append(out, strings.TrimSpace(item.Value))
		}
		return out
	default:
		return nil
	}
}

// decodeDependsOn decodes depends_on, accepting both the list form and the
// mapping form (with optional condition metadata, which is ignored here).
func decodeDependsOn(n *yamlNode) ([]string, error) {
	if n == nil || n.Kind == 0 {
		return nil, nil
	}
	switch n.Kind {
	case yamlSequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			if name := strings.TrimSpace(item.Value); name != "" {
				out = append(out, name)
			}
		}
		return out, nil
	case yamlMappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out, nil
	default:
		return nil, errors.New("must be a sequence or mapping")
	}
}

// decodeDuration parses a Compose duration string. Compose accepts Go-style
// durations (e.g. "30s", "1m", "1h30m") and plain integers meaning seconds;
// the latter is accepted for compatibility with older Compose files.
func decodeDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Fall back to seconds, as Compose does for bare integers.
	if n := parseInt(s); n >= 0 && s != "" {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration %q", s)
}

// parseInt parses a non-negative integer.
func parseInt(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// unquote trims matching surrounding quotes from a scalar value.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
