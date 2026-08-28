package compose

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"accorda/internal/config"
)

// renderDeployCompose reads the source Compose file, merges per-service env
// overrides from accorda.yaml into each named service's environment:, stamps
// the Accorda ownership label (accordaManagedLabel) on every service, and
// writes the result to a deploy Compose file alongside the source. It always
// renders a deploy file: the ownership label is what lets Accorda later prove
// a container is its own when reclaiming stale containers (docs/DECISIONS.md
// #54). The deploy file is written next to the source so Compose's
// relative-path resolution (build contexts, volumes, env_file) still resolves
// against the same checkout root (docs/DECISIONS.md #23).
func renderDeployCompose(sourceFile string, overrides map[string]config.ServiceOverride, deploymentID string) (string, error) {
	data, err := os.ReadFile(sourceFile)
	if err != nil {
		return "", fmt.Errorf("compose: read source for deploy render: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("compose: parse source for deploy render: %w", err)
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("compose: deploy render: no services map in %q", sourceFile)
	}
	mergedEnv, err := resolveOverrides(overrides)
	if err != nil {
		return "", err
	}
	for name, svc := range services {
		m, ok := svc.(map[string]any)
		if !ok {
			continue
		}
		if env, ok := mergedEnv[name]; ok {
			m["environment"] = mergeServiceEnv(m["environment"], env)
		}
		m["labels"] = withAccordaLabels(m["labels"], deploymentID)
		services[name] = m
	}
	doc["services"] = services
	deployFile := deployComposePath(sourceFile)
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("compose: marshal deploy file: %w", err)
	}
	if err := os.WriteFile(deployFile, out, 0o600); err != nil {
		return "", fmt.Errorf("compose: write deploy file: %w", err)
	}
	return deployFile, nil
}

// withAccordaLabels returns the given labels map (Compose labels: may be a map
// or a list of KEY=VALUE strings) with the Accorda ownership label added and,
// when a deployment ID is supplied, the deployment identifier label added.
// Existing labels are preserved. Service labels declared in the Compose file
// are stamped onto the container by Compose, so this marks the container as
// owned by Accorda and ties it to its deployment at creation time.
func withAccordaLabels(existing any, deploymentID string) any {
	labels := normalizeLabelsValue(existing)
	labels[accordaManagedLabel] = "true"
	if deploymentID != "" {
		labels[accordaDeploymentLabel] = deploymentID
	}
	// Compose accepts a map form for labels.
	out := make(map[string]string, len(labels))
	maps.Copy(out, labels)
	return out
}

// normalizeLabelsValue converts Compose's labels declaration (a map, a list of
// KEY=VALUE strings, or absent) into a map.
func normalizeLabelsValue(v any) map[string]string {
	switch l := v.(type) {
	case map[string]string:
		return l
	case map[string]any:
		out := make(map[string]string, len(l))
		for k, val := range l {
			out[k] = fmt.Sprint(val)
		}
		return out
	case []string:
		out := make(map[string]string, len(l))
		for _, e := range l {
			if k, val, found := strings.Cut(e, "="); found {
				out[k] = val
			}
		}
		return out
	case []any:
		out := map[string]string{}
		for _, e := range l {
			if s, ok := e.(string); ok {
				if k, val, found := strings.Cut(s, "="); found {
					out[k] = val
				}
			}
		}
		return out
	default:
		return map[string]string{}
	}
}

// deployComposePath returns the path for the rendered deploy Compose file,
// placed next to the source so relative resources resolve identically.
const deployFileSuffix = ".accorda-deploy.yml"

func deployComposePath(sourceFile string) string {
	return filepath.Join(filepath.Dir(sourceFile), deployFileSuffix)
}

// cleanupDeployFile removes the rendered deploy Compose file after Apply
// completes (success or failure) so the managed checkout is not polluted
// with a secret-bearing artifact (docs/DECISIONS.md #23). It is a no-op when
// the deploy file is the source file (no overrides were rendered).
func cleanupDeployFile(deployFile, sourceFile string) {
	if deployFile == sourceFile {
		return
	}
	_ = os.Remove(deployFile)
}

// resolveOverrides reads env_files and merges their entries with inline env:
// values for each overridden service. Precedence (low → high): env_files
// entries in list order, then inline env: values. The result is a single
// map[string]string per service ready to merge into the Compose environment:.
func resolveOverrides(overrides map[string]config.ServiceOverride) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string, len(overrides))
	for name, svc := range overrides {
		env := make(map[string]string)
		for _, ref := range svc.EnvFiles {
			entries, err := parseEnvFile(ref.Path)
			if err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			for _, e := range entries {
				env[e.key] = e.value
			}
		}
		for k, v := range svc.Env {
			env[k] = v
		}
		if len(env) > 0 {
			result[name] = env
		}
	}
	return result, nil
}

// mergeServiceEnv merges resolved override values into a service's existing
// environment: field. The existing field may be a map (long form) or a list
// of KEY=VALUE strings (short form); both are normalized to a map. Override
// values take precedence over existing values on key collision.
func mergeServiceEnv(existing any, overrides map[string]string) map[string]string {
	merged := normalizeExistingEnv(existing)
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// normalizeExistingEnv converts a Compose environment: field into a
// map[string]string regardless of its YAML representation: a map (long form)
// or a list of KEY=VALUE strings (short form). Nil and unrecognized types
// yield an empty map.
func normalizeExistingEnv(existing any) map[string]string {
	merged := make(map[string]string)
	switch env := existing.(type) {
	case map[string]any:
		for k, v := range env {
			merged[k] = fmt.Sprint(v)
		}
	case map[string]string:
		for k, v := range env {
			merged[k] = v
		}
	case []any:
		for _, entry := range env {
			k, v, ok := splitEnvEntry(fmt.Sprint(entry))
			if !ok {
				continue
			}
			merged[k] = v
		}
	case []string:
		for _, s := range env {
			k, v, ok := splitEnvEntry(s)
			if !ok {
				continue
			}
			merged[k] = v
		}
	}
	return merged
}

// splitEnvEntry splits a "KEY=VALUE" string into key and value, returning
// ok=false when the line has no '='.
func splitEnvEntry(s string) (key, value string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
