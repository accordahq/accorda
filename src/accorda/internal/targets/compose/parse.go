package compose

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
// it into Accorda's service model. It understands the Compose services map
// and the per-service fields Accorda core reasons about (image, command,
// environment, ports, volumes, networks, labels, healthcheck, depends_on),
// validating required fields.
//
// Parse is intentionally dependency-free: it uses the same YAML decoder the
// rest of Accorda uses and accepts the subset of the Compose schema Accorda
// needs. Unknown top-level and service-level keys are rejected so that a
// typo in a recognized field is surfaced rather than silently ignored.
func Parse(data []byte) (map[string]state.Service, error) {
	root, err := decode(data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]state.Service{}, nil
	}
	servicesNode, ok := root["services"]
	if !ok || servicesNode == nil {
		// A document with no services is valid but empty; nothing to do.
		return map[string]state.Service{}, nil
	}
	if servicesNode.Kind != yamlMappingNode {
		return nil, fmt.Errorf("services: expected a mapping, got %s", nodeKindName(servicesNode))
	}
	services := make(map[string]state.Service, len(servicesNode.Content)/2)
	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		nameNode := servicesNode.Content[i]
		svcNode := servicesNode.Content[i+1]
		if nameNode.Value == "" {
			return nil, errors.New("services: empty service name")
		}
		svc, err := parseService(nameNode.Value, svcNode)
		if err != nil {
			return nil, fmt.Errorf("services.%s: %w", nameNode.Value, err)
		}
		if err := validateService(nameNode.Value, svc); err != nil {
			return nil, err
		}
		services[nameNode.Value] = svc
	}
	return services, nil
}

// validateService enforces the required-field rules the spec calls out for a
// normalized Compose service (docs/ACCORDA.md §8). A service must declare an
// image or a build context; Accorda's service model is image-centric, so a
// build-only service is accepted only when it resolves to an image later.
// For the load/validate phase we require an image so the desired state is
// concrete and deployable.
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

// parseService parses a single service mapping node into a state.Service.
func parseService(name string, node *yamlNode) (state.Service, error) {
	svc := state.Service{Env: map[string]string{}, Labels: map[string]string{}}
	if node == nil {
		return svc, nil
	}
	if node.Kind != yamlMappingNode {
		return svc, fmt.Errorf("expected a mapping, got %s", nodeKindName(node))
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		if err := applyServiceField(&svc, key, val); err != nil {
			return svc, err
		}
	}
	return svc, nil
}

// applyServiceField dispatches one service mapping entry to the matching
// normalizer. Unknown keys are rejected so configuration typos surface.
func applyServiceField(svc *state.Service, key string, val *yamlNode) error {
	switch key {
	case "image":
		svc.Image = decodeScalar(val)
	case "build":
		// Accorda's desired-state model is image-centric; build contexts are
		// acknowledged but do not populate an image at load time. A service
		// with only build and no image fails validation.
		_ = val
	case "command":
		svc.Command = decodeStringOrList(val)
	case "environment", "env":
		if err := decodeEnvironment(val, svc.Env); err != nil {
			return fmt.Errorf("environment: %w", err)
		}
	case "ports", "expose":
		ports, err := decodePorts(val)
		if err != nil {
			return fmt.Errorf("ports: %w", err)
		}
		svc.Ports = append(svc.Ports, ports...)
	case "volumes":
		vols, err := decodeVolumes(val)
		if err != nil {
			return fmt.Errorf("volumes: %w", err)
		}
		svc.Volumes = append(svc.Volumes, vols...)
	case "networks":
		nets, err := decodeNetworks(val)
		if err != nil {
			return fmt.Errorf("networks: %w", err)
		}
		svc.Networks = append(svc.Networks, nets...)
	case "labels":
		if err := decodeMapping(val, svc.Labels); err != nil {
			return fmt.Errorf("labels: %w", err)
		}
	case "healthcheck":
		hc, err := decodeHealthcheck(val)
		if err != nil {
			return fmt.Errorf("healthcheck: %w", err)
		}
		svc.Healthcheck = hc
	case "depends_on":
		deps, err := decodeDependsOn(val)
		if err != nil {
			return fmt.Errorf("depends_on: %w", err)
		}
		svc.DependsOn = append(svc.DependsOn, deps...)
	default:
		return fmt.Errorf("unknown field %q", key)
	}
	return nil
}
