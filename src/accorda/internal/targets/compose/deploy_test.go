package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"accorda/internal/config"
)

func TestRenderDeployCompose_NoOverrides_StillStampsOwnershipLabel(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
    environment:
      MODE: production
`)
	deployFile, err := renderDeployCompose(source, nil, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	if deployFile == source {
		t.Fatal("deploy file path should differ from source even with no overrides (ownership label)")
	}
	svc := readDeployService(t, deployFile, "api")
	labels, ok := svc["labels"].(map[string]any)
	if !ok {
		t.Fatalf("deploy service %q missing labels map: %v", "api", svc["labels"])
	}
	if got := labels[accordaManagedLabel]; got != "true" {
		t.Errorf("accorda.managed label = %v, want true", got)
	}
	if got := labels[accordaDeploymentLabel]; got != "dep_abc" {
		t.Errorf("accorda.deployment_id label = %v, want dep_abc", got)
	}
	// The env override path must not clobber the ownership label either.
	if _, ok := svc["environment"]; !ok {
		t.Errorf("environment should be preserved when no overrides")
	}
}

func TestRenderDeployCompose_EmptyDeploymentID_Omitted(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	deployFile, err := renderDeployCompose(source, nil, "")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	svc := readDeployService(t, deployFile, "api")
	labels, ok := svc["labels"].(map[string]any)
	if !ok {
		t.Fatalf("deploy service %q missing labels map: %v", "api", svc["labels"])
	}
	// Ownership label is always present; deployment ID is omitted when empty.
	if got := labels[accordaManagedLabel]; got != "true" {
		t.Errorf("accorda.managed label = %v, want true", got)
	}
	if _, present := labels[accordaDeploymentLabel]; present {
		t.Errorf("accorda.deployment_id label should be omitted when deployment ID is empty")
	}
}

func TestRenderDeployCompose_MergesInlineEnv(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
    environment:
      MODE: production
      KEEP: original
`)
	overrides := map[string]config.ServiceOverride{
		"api": {Env: map[string]string{"MODE": "staging", "NEW_KEY": "added"}},
	}
	deployFile, err := renderDeployCompose(source, overrides, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	if deployFile == source {
		t.Fatal("deploy file path should differ from source when overrides present")
	}
	svc := readDeployServiceEnv(t, deployFile, "api")
	// Override wins for MODE, KEEP is preserved, NEW_KEY is added.
	if svc["MODE"] != "staging" {
		t.Errorf("MODE = %q, want staging (override)", svc["MODE"])
	}
	if svc["KEEP"] != "original" {
		t.Errorf("KEEP = %q, want original (preserved)", svc["KEEP"])
	}
	if svc["NEW_KEY"] != "added" {
		t.Errorf("NEW_KEY = %q, want added", svc["NEW_KEY"])
	}
}

func TestRenderDeployCompose_MergesEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "secrets.env")
	if err := os.WriteFile(envFile, []byte("SECRET_KEY=topsecret\nPORT=8080\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
    environment:
      PORT: "3000"
`)
	overrides := map[string]config.ServiceOverride{
		"api": {EnvFiles: []config.EnvFileRef{{Path: envFile}}},
	}
	deployFile, err := renderDeployCompose(source, overrides, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	svc := readDeployServiceEnv(t, deployFile, "api")
	// File value overrides compose value, new key added.
	if svc["PORT"] != "8080" {
		t.Errorf("PORT = %q, want 8080 (from env_file)", svc["PORT"])
	}
	if svc["SECRET_KEY"] != "topsecret" {
		t.Errorf("SECRET_KEY = %q, want topsecret (from env_file)", svc["SECRET_KEY"])
	}
}

func TestRenderDeployCompose_InlineOverridesEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "secrets.env")
	if err := os.WriteFile(envFile, []byte("KEY=from_file\nOTHER=file_value\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	overrides := map[string]config.ServiceOverride{
		"api": {
			Env:      map[string]string{"KEY": "from_inline"},
			EnvFiles: []config.EnvFileRef{{Path: envFile}},
		},
	}
	deployFile, err := renderDeployCompose(source, overrides, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	svc := readDeployServiceEnv(t, deployFile, "api")
	// Inline env takes precedence over env_file on collision.
	if svc["KEY"] != "from_inline" {
		t.Errorf("KEY = %q, want from_inline (inline wins)", svc["KEY"])
	}
	if svc["OTHER"] != "file_value" {
		t.Errorf("OTHER = %q, want file_value (from env_file)", svc["OTHER"])
	}
}

func TestRenderDeployCompose_SkipsUnknownService(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	overrides := map[string]config.ServiceOverride{
		"nonexistent": {Env: map[string]string{"KEY": "val"}},
	}
	deployFile, err := renderDeployCompose(source, overrides, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	// Should still produce a deploy file without error.
	data, err := os.ReadFile(deployFile)
	if err != nil {
		t.Fatalf("read deploy file: %v", err)
	}
	if string(data) == "" {
		t.Fatal("deploy file should not be empty")
	}
}

func writeSourceCompose(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write source compose: %v", err)
	}
	return path
}

func readDeployServiceEnv(t *testing.T, deployFile, service string) map[string]string {
	t.Helper()
	svc := readDeployService(t, deployFile, service)
	env, ok := svc["environment"].(map[string]any)
	if !ok {
		t.Fatalf("service %q has no environment map", service)
	}
	result := make(map[string]string, len(env))
	for k, v := range env {
		result[k] = stringOf(v)
	}
	return result
}

// readDeployService reads the deploy file and returns the raw map form of one
// service so tests can assert any field (env, labels, etc.).
func readDeployService(t *testing.T, deployFile, service string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(deployFile)
	if err != nil {
		t.Fatalf("read deploy file: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal deploy file: %v", err)
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		t.Fatal("no services in deploy file")
	}
	svc, ok := services[service].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in deploy file", service)
	}
	return svc
}

func stringOf(v any) string {
	return fmt.Sprint(v)
}

func TestRenderDeployCompose_DeployFilePermissions(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	overrides := map[string]config.ServiceOverride{
		"api": {Env: map[string]string{"KEY": "val"}},
	}
	deployFile, err := renderDeployCompose(source, overrides, "dep_abc")
	if err != nil {
		t.Fatalf("renderDeployCompose: %v", err)
	}
	info, err := os.Stat(deployFile)
	if err != nil {
		t.Fatalf("stat deploy file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("deploy file mode = %04o, want 0600 (secrets may be present)", info.Mode().Perm())
	}
	cleanupDeployFile(deployFile, source)
	if _, err := os.Stat(deployFile); !os.IsNotExist(err) {
		t.Errorf("deploy file should be removed after cleanup, stat err = %v", err)
	}
}

func TestCleanupDeployFile_NoOpForSourceFile(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	// When deployFile == sourceFile (no overrides), cleanup should not remove it.
	cleanupDeployFile(source, source)
	if _, err := os.Stat(source); err != nil {
		t.Errorf("source file should not be removed by cleanup: %v", err)
	}
}

func TestRenderDeployCompose_MissingEnvFileIsError(t *testing.T) {
	source := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	overrides := map[string]config.ServiceOverride{
		"api": {EnvFiles: []config.EnvFileRef{{Path: "/nonexistent/path/file.env"}}},
	}
	_, err := renderDeployCompose(source, overrides, "dep_abc")
	if err == nil {
		t.Fatal("renderDeployCompose with missing env file should return error")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing file: %v", err)
	}
}
