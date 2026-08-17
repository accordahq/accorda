package git

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"accorda/internal/core/state"
)

// parseComposeServices is a minimal, dependency-free parser for the
// Compose-style services file shown in docs/ACCORDA.md §9. It understands
// the flat mapping form:
//
//	services:
//	  api:
//	    image: ghcr.io/acme/api:1.9
//	    environment:
//	      LOG_LEVEL: warning
//
// It does not implement the full Docker Compose schema; it only extracts the
// fields Accorda core needs to reason about desired state (image and
// environment per service). Unknown keys are ignored so that real compose
// files with ports, depends_on, etc. do not cause failures.
type serviceBlock struct {
	name   string
	indent int
}

func parseComposeServices(data []byte) (map[string]state.Service, error) {
	services := make(map[string]state.Service)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		inServices     bool
		servicesIndent int
		current        *serviceBlock
		envIndent      = -1 // active env-block indent, -1 when inactive
	)
	_ = servicesIndent

	for scanner.Scan() {
		line := scanner.Text()
		// Drop comments and trailing whitespace.
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := leadingSpaces(trimmed)
		content := strings.TrimSpace(trimmed)

		// Close an active env block when indentation returns to the service
		// level or above.
		if envIndent >= 0 && current != nil && indent <= current.indent {
			envIndent = -1
		}

		if indent == 0 {
			inServices = content == "services:"
			current = nil
			envIndent = -1
			continue
		}

		if !inServices {
			continue
		}

		// Inside services: handle service name lines, env blocks, and kv
		// fields relative to the current service's indentation.
		switch {
		case current == nil && strings.HasSuffix(content, ":") && !strings.Contains(content, ": "):
			name := strings.TrimSpace(strings.TrimSuffix(content, ":"))
			if name == "" {
				return nil, fmt.Errorf("services: empty service name")
			}
			current = &serviceBlock{name: name, indent: indent}
			services[name] = state.Service{Env: map[string]string{}}
			continue
		case current == nil:
			continue
		}

		// A new service-level key starts when we return to the service's
		// indentation.
		if indent == current.indent && strings.HasSuffix(content, ":") && !strings.Contains(content, ": ") {
			name := strings.TrimSpace(strings.TrimSuffix(content, ":"))
			if name == "" {
				return nil, fmt.Errorf("services: empty service name")
			}
			current = &serviceBlock{name: name, indent: indent}
			services[name] = state.Service{Env: map[string]string{}}
			envIndent = -1
			continue
		}

		if envIndent >= 0 {
			// Inside an environment block: accept "- KEY=VALUE" list items
			// or "KEY: VALUE" mapping entries.
			if strings.HasPrefix(content, "-") {
				val := strings.TrimSpace(strings.TrimPrefix(content, "-"))
				svc := services[current.name]
				svc.Env = setEnv(svc.Env, val)
				services[current.name] = svc
				continue
			}
			if key, val, ok := splitKV(content); ok {
				svc := services[current.name]
				svc.Env[key] = val
				services[current.name] = svc
				continue
			}
			continue
		}

		key, val, ok := splitKV(content)
		if !ok {
			continue
		}
		switch key {
		case "image":
			svc := services[current.name]
			svc.Image = val
			services[current.name] = svc
		case "environment":
			if val == "" {
				// Block form follows at a deeper indentation than the
				// service. Record the field's indentation so subsequent
				// deeper-indented lines are parsed as env entries.
				envIndent = indent
			} else {
				svc := services[current.name]
				svc.Env = setEnv(svc.Env, val)
				services[current.name] = svc
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("services: scan: %w", err)
	}

	// Drop services with no image and no env so the desired state is clean;
	// callers that want every declared service can add them back later.
	for name, svc := range services {
		if svc.Image == "" && len(svc.Env) == 0 {
			delete(services, name)
		}
	}
	return services, nil
}

// splitKV splits "key: value" or "key:" into key and value.
func splitKV(s string) (string, string, bool) {
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:i])
	val := strings.TrimSpace(s[i+1:])
	if key == "" {
		return "", "", false
	}
	val = strings.Trim(val, `"'`)
	return key, val, true
}

// setEnv records a KEY=VALUE or KEY entry into env. If only KEY is given the
// value is set to the empty string, matching Compose's environment list form.
func setEnv(env map[string]string, raw string) map[string]string {
	if env == nil {
		env = map[string]string{}
	}
	raw = strings.Trim(raw, `"'`)
	if i := strings.Index(raw, "="); i >= 0 {
		env[raw[:i]] = raw[i+1:]
	} else {
		env[raw] = ""
	}
	return env
}

// leadingSpaces counts the leading spaces of s. Tabs count as one space each
// for the purposes of this minimal parser; mixing tabs and spaces in the
// same indentation is not supported.
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n++
		default:
			return n
		}
	}
	return n
}
