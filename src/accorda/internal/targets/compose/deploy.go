package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"accorda/internal/config"
)

// renderDeployCompose reads the source Compose file, merges per-service env
// overrides from accorda.yaml into each named service's environment:, and
// writes the result to a deploy Compose file alongside the source. It returns
// the deploy file path, or the source path unchanged when no overrides are
// configured (docs/DECISIONS.md #45).
//
// The deploy file carries only the merged environment: entries for overridden
// services; all other services and fields are preserved verbatim from the
// source so `docker compose up -d` sees the full project. The deploy file is
// written next to the source so Compose's relative-path resolution (build
// contexts, volumes, env_file) still resolves against the same checkout root.
func renderDeployCompose(sourceFile string, overrides map[string]config.ServiceOverride) (string, error) {
	if len(overrides) == 0 {
		return sourceFile, nil
	}
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
	mergedEnv := resolveOverrides(overrides)
	for name, env := range mergedEnv {
		svc, ok := services[name].(map[string]any)
		if !ok {
			// The override targets a service not in the Compose file; skip it
			// rather than inventing a service, since Accorda's plan would not
			// have included it either.
			continue
		}
		svc["environment"] = mergeServiceEnv(svc["environment"], env)
		services[name] = svc
	}
	doc["services"] = services
	deployFile := deployComposePath(sourceFile)
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("compose: marshal deploy file: %w", err)
	}
	if err := os.WriteFile(deployFile, out, 0o644); err != nil {
		return "", fmt.Errorf("compose: write deploy file: %w", err)
	}
	return deployFile, nil
}

// deployComposePath returns the path for the rendered deploy Compose file,
// placed next to the source so relative resources resolve identically.
const deployFileSuffix = ".accorda-deploy.yml"

func deployComposePath(sourceFile string) string {
	return filepath.Join(filepath.Dir(sourceFile), deployFileSuffix)
}

// resolveOverrides reads env_files and merges their entries with inline env:
// values for each overridden service. Precedence (low → high): env_files
// entries in list order, then inline env: values. The result is a single
// map[string]string per service ready to merge into the Compose environment:.
func resolveOverrides(overrides map[string]config.ServiceOverride) map[string]map[string]string {
	result := make(map[string]map[string]string, len(overrides))
	for name, svc := range overrides {
		env := make(map[string]string)
		for _, ref := range svc.EnvFiles {
			entries, err := parseEnvFile(ref.Path)
			if err != nil {
				continue
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
	return result
}

// mergeServiceEnv merges resolved override values into a service's existing
// environment: field. The existing field may be a map (long form) or a list
// of KEY=VALUE strings (short form); both are normalized to a map. Override
// values take precedence over existing values on key collision.
func mergeServiceEnv(existing any, overrides map[string]string) map[string]string {
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
			s := fmt.Sprint(entry)
			k, v, ok := splitEnvEntry(s)
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
	case nil:
		// No existing environment; start from overrides only.
	}
	for k, v := range overrides {
		merged[k] = v
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
