package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	nestedComposeFile    = "deploy/" + DefaultComposeFile
	composeTargetFixture = "target: {type: " + TargetCompose + ", file: c.yaml}\n"
)

// composeExample is the example from docs/ACCORDA.md §8 (Docker Compose
// Target), extended with the version/environment fields from §25 so it is a
// complete, valid project file.
const composeExample = `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
  branch: production
  path: services/api
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
sync:
  interval: 30s
images:
  pull: ` + PullChanged + `
reconcile:
  drift: ` + DriftRepair + `
  remove_orphans: true
health:
  timeout: 120s
`

// composeExample25 is the Compose example from docs/ACCORDA.md §25 (Unified
// Project Format). The spec example omits source.url because it illustrates
// the long-term unified format concept; a complete, loadable project file
// requires a Git URL, so it is added here.
const composeExample25 = `version: 1
environment: production
source:
  url: git@github.com:acme/infra.git
  branch: main
target:
  type: ` + TargetCompose + `
  path: ` + nestedComposeFile + `
secrets:
  - deploy/prod.env.sops
health:
  timeout: 120s
notifications:
  github: true
`

// kubernetesExample25 is the Kubernetes example from docs/ACCORDA.md §25,
// extended with source.url for the same reason as composeExample25.
const kubernetesExample25 = `version: 1
environment: production
source:
  url: git@github.com:acme/infra.git
  branch: main
target:
  type: ` + TargetKubernetes + `
  path: deploy/kubernetes
secrets:
  provider: sops
health:
  timeout: 300s
`

func TestParse_ComposeExample(t *testing.T) {
	p, err := Parse([]byte(composeExample))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Version != 1 {
		t.Errorf("Version = %d, want 1", p.Version)
	}
	if p.Environment != "production" {
		t.Errorf("Environment = %q, want %q", p.Environment, "production")
	}
	if p.Source.Type != "git" {
		t.Errorf("Source.Type = %q, want %q", p.Source.Type, "git")
	}
	if p.Source.URL != "git@github.com:acme/infra.git" {
		t.Errorf("Source.URL = %q, want %q", p.Source.URL, "git@github.com:acme/infra.git")
	}
	if p.Source.Branch != "production" {
		t.Errorf("Source.Branch = %q, want %q", p.Source.Branch, "production")
	}
	if p.Source.Path != "services/api" {
		t.Errorf("Source.Path = %q, want %q", p.Source.Path, "services/api")
	}
	if p.Target.Type != TargetCompose {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, TargetCompose)
	}
	if p.Target.File != DefaultComposeFile {
		t.Errorf("Target.File = %q, want %q", p.Target.File, DefaultComposeFile)
	}
	if p.Sync.Interval != 30*time.Second {
		t.Errorf("Sync.Interval = %v, want %v", p.Sync.Interval, 30*time.Second)
	}
	if p.Images.Pull != PullChanged {
		t.Errorf("Images.Pull = %q, want %q", p.Images.Pull, PullChanged)
	}
	if p.Reconcile.Drift != DriftRepair {
		t.Errorf("Reconcile.Drift = %q, want %q", p.Reconcile.Drift, DriftRepair)
	}
	if p.Reconcile.RemoveOrphans == nil || !*p.Reconcile.RemoveOrphans {
		t.Errorf("Reconcile.RemoveOrphans = %v, want true", p.Reconcile.RemoveOrphans)
	}
	if p.Health.Timeout != 120*time.Second {
		t.Errorf("Health.Timeout = %v, want %v", p.Health.Timeout, 120*time.Second)
	}
}

func TestParse_ComposeExample25(t *testing.T) {
	p, err := Parse([]byte(composeExample25))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Target.Type != TargetCompose {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, TargetCompose)
	}
	if p.Target.Path != nestedComposeFile {
		t.Errorf("Target.Path = %q, want %q", p.Target.Path, nestedComposeFile)
	}
	if len(p.Secrets.Files) != 1 || p.Secrets.Files[0] != "deploy/prod.env.sops" {
		t.Errorf("Secrets.Files = %v, want [deploy/prod.env.sops]", p.Secrets.Files)
	}
	if !p.Notifications.GitHub {
		t.Errorf("Notifications.GitHub = false, want true")
	}
	// Defaults applied for omitted fields.
	if p.Source.Type != "git" {
		t.Errorf("Source.Type default = %q, want %q", p.Source.Type, "git")
	}
	if p.Source.Branch != "main" {
		t.Errorf("Source.Branch default = %q, want %q", p.Source.Branch, "main")
	}
	if p.Images.Pull != PullChanged {
		t.Errorf("Images.Pull default = %q, want %q", p.Images.Pull, PullChanged)
	}
}

func TestParse_KubernetesExample25(t *testing.T) {
	p, err := Parse([]byte(kubernetesExample25))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Target.Type != TargetKubernetes {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, TargetKubernetes)
	}
	if p.Target.Path != "deploy/kubernetes" {
		t.Errorf("Target.Path = %q, want %q", p.Target.Path, "deploy/kubernetes")
	}
	if p.Secrets.Provider != "sops" {
		t.Errorf("Secrets.Provider = %q, want %q", p.Secrets.Provider, "sops")
	}
	if p.Health.Timeout != 300*time.Second {
		t.Errorf("Health.Timeout = %v, want %v", p.Health.Timeout, 300*time.Second)
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte(composeExample), 0o644); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if p.Environment != "production" {
		t.Errorf("Environment = %q, want %q", p.Environment, "production")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), File) {
		t.Fatalf("Load: error should mention %q, got %v", File, err)
	}
}

func TestLoad_InvalidProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte("version: ["), 0o600); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("Load() error = %v, want parse failure", err)
	}
}

func TestLoad_RejectsReadableCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	data := strings.Replace(composeExample, "  path: services/api\n", `  path: services/api
  auth:
    type: https
    token: token-super-secret
`, 1)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "permissions 0600") {
		t.Fatalf("Load() error = %v, want restrictive-permissions failure", err)
	}
	if strings.Contains(err.Error(), "token-super-secret") {
		t.Fatalf("Load() error leaked token: %v", err)
	}
}

func TestLoad_AcceptsPrivateCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	data := strings.Replace(composeExample, "  path: services/api\n", `  path: services/api
  auth:
    type: https
    token: token-super-secret
`, 1)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
}

func TestLoad_RejectsReadableURLCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	data := strings.Replace(composeExample,
		"git@github.com:acme/infra.git", "https://user:token-super-secret@git.internal/acme/infra.git", 1)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write project file: %v", err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "permissions 0600") {
		t.Fatalf("Load() error = %v, want restrictive-permissions failure", err)
	}
	if strings.Contains(err.Error(), "token-super-secret") {
		t.Fatalf("Load() error leaked token: %v", err)
	}
}

func TestValidateCredentialFileMode_Errors(t *testing.T) {
	if err := validateCredentialFileMode("missing", nil); err != nil {
		t.Fatalf("validateCredentialFileMode(nil) = %v, want nil", err)
	}
	project := &Project{Source: Source{Auth: Auth{Token: "secret"}}}
	err := validateCredentialFileMode(filepath.Join(t.TempDir(), "missing"), project)
	if err == nil || !strings.Contains(err.Error(), "inspect permissions") {
		t.Fatalf("validateCredentialFileMode(missing) = %v, want stat failure", err)
	}
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	src := composeExample + "bogus_field: oops\n"
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse: expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Fatalf("Parse: error should mention the unknown field, got %v", err)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte("version: 1\nenvironment: [oops\n"))
	if err == nil {
		t.Fatal("Parse: expected error for malformed YAML, got nil")
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing version",
			yaml: "environment: production\nsource: {url: x}\n" + composeTargetFixture,
			want: "version is required",
		},
		{
			name: "unsupported version",
			yaml: "version: 2\nenvironment: production\nsource: {url: x}\n" + composeTargetFixture,
			want: "version 2 is not supported",
		},
		{
			name: "missing environment",
			yaml: "version: 1\nsource: {url: x}\n" + composeTargetFixture,
			want: "environment is required",
		},
		{
			name: "missing source url",
			yaml: "version: 1\nenvironment: production\n" + composeTargetFixture,
			want: "source.url is required",
		},
		{
			name: "unsupported source type",
			yaml: "version: 1\nenvironment: production\nsource: {type: s3, url: x}\n" + composeTargetFixture,
			want: "source.type \"s3\" is not supported",
		},
		{
			name: "missing target type",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\n",
			want: "target.type is required",
		},
		{
			name: "compose without file or path",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: " + TargetCompose + "}\n",
			want: "target.file or target.path is required for \"" + TargetCompose + "\" targets",
		},
		{
			name: "kubernetes without path",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: " + TargetKubernetes + "}\n",
			want: "target.path is required for \"" + TargetKubernetes + "\" targets",
		},
		{
			name: "unsupported target type",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: nomad}\n",
			want: "target.type \"nomad\" is not supported",
		},
		{
			name: "invalid image pull",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\n" + composeTargetFixture + "images: {pull: sometimes}\n",
			want: "images.pull \"sometimes\" is not valid",
		},
		{
			name: "invalid drift",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\n" + composeTargetFixture + "reconcile: {drift: fix}\n",
			want: "reconcile.drift \"fix\" is not valid",
		},
		{
			name: "secrets empty file entry",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\n" + composeTargetFixture + "secrets: [\"\"]\n",
			want: "secrets.files[0] is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestParse_Defaults(t *testing.T) {
	src := `version: 1
environment: production
source:
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Source.Type != "git" {
		t.Errorf("Source.Type default = %q, want %q", p.Source.Type, "git")
	}
	if p.Source.Branch != "main" {
		t.Errorf("Source.Branch default = %q, want %q", p.Source.Branch, "main")
	}
	if p.Images.Pull != PullChanged {
		t.Errorf("Images.Pull default = %q, want %q", p.Images.Pull, PullChanged)
	}
	if p.Reconcile.Drift != DriftReport {
		t.Errorf("Reconcile.Drift default = %q, want %q", p.Reconcile.Drift, DriftReport)
	}
	if p.Health.Timeout != 120*time.Second {
		t.Errorf("Health.Timeout default = %v, want %v", p.Health.Timeout, 120*time.Second)
	}
	if p.Sync.Interval != 30*time.Second {
		t.Errorf("Sync.Interval default = %v, want %v", p.Sync.Interval, 30*time.Second)
	}
	if p.Reconcile.RemoveOrphans != nil {
		t.Errorf("Reconcile.RemoveOrphans default = %v, want nil", p.Reconcile.RemoveOrphans)
	}
}

func TestValidate_Nil(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Fatal("Validate(nil): expected error, got nil")
	}
}

func TestParse_AuthSSH(t *testing.T) {
	src := `version: 1
environment: production
source:
  url: git@git.internal:acme/infra.git
  branch: main
  auth:
    type: ` + AuthSSH + `
    key: /etc/Accorda/git.key
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Source.Auth.Type != AuthSSH {
		t.Errorf("Auth.Type = %q, want %q", p.Source.Auth.Type, AuthSSH)
	}
	if p.Source.Auth.Key != "/etc/Accorda/git.key" {
		t.Errorf("Auth.Key = %q, want /etc/Accorda/git.key", p.Source.Auth.Key)
	}
}

func TestParse_AuthHTTPS(t *testing.T) {
	src := `version: 1
environment: production
source:
  url: https://git.internal/acme/infra.git
  branch: main
  auth:
    type: ` + AuthHTTPS + `
    token: ghp_secrettoken
    username: x-access-token
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Source.Auth.Type != AuthHTTPS {
		t.Errorf("Auth.Type = %q, want %q", p.Source.Auth.Type, AuthHTTPS)
	}
	if p.Source.Auth.Token != "ghp_secrettoken" {
		t.Errorf("Auth.Token = %q, want ghp_secrettoken", p.Source.Auth.Token)
	}
	if p.Source.Auth.Username != "x-access-token" {
		t.Errorf("Auth.Username = %q, want x-access-token", p.Source.Auth.Username)
	}
}

func TestValidate_AuthErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "ssh without key",
			yaml: `version: 1
environment: production
source:
  url: https://git.internal/acme/infra.git
  branch: main
  auth:
    type: ` + AuthSSH + `
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`,
			want: "source.auth.key is required",
		},
		{
			name: "https without token",
			yaml: `version: 1
environment: production
source:
  url: https://git.internal/acme/infra.git
  branch: main
  auth:
    type: ` + AuthHTTPS + `
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`,
			want: "source.auth.token is required",
		},
		{
			name: "unsupported auth type",
			yaml: `version: 1
environment: production
source:
  url: https://git.internal/acme/infra.git
  branch: main
  auth:
    type: basic
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`,
			want: `source.auth.type "basic" is not supported`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestParse_AuthEmptyIsValid(t *testing.T) {
	// An absent auth section is valid and means "use ambient environment".
	src := `version: 1
environment: production
source:
  url: https://git.internal/acme/infra.git
  branch: main
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`
	_, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error for absent auth: %v", err)
	}
}

func TestMarshalProject_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "compose example (secrets list form)",
			yaml: composeExample25,
		},
		{
			name: "kubernetes example (secrets provider form)",
			yaml: kubernetesExample25,
		},
		{
			name: "init-shaped minimal project",
			yaml: `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/backend.git
  branch: main
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertRoundTrip(t, c.yaml)
		})
	}
}

// assertRoundTrip parses yaml, marshals it back, re-parses, and checks that
// the fields that matter for reconciliation match. It is extracted from
// TestMarshalProject_RoundTrip to keep the test function below the cognitive
// complexity limit (go:S3776).
func assertRoundTrip(t *testing.T, yaml string) {
	t.Helper()
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := MarshalProject(p)
	if err != nil {
		t.Fatalf("MarshalProject: %v", err)
	}
	// The marshaled document must re-parse through the strict loader.
	p2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-Parse of marshaled output failed: %v\noutput:\n%s", err, out)
	}
	assertProjectEqual(t, p, p2)
}

// assertProjectEqual checks that two Projects match on the fields that matter
// for reconciliation. Extracted to reduce cognitive complexity (go:S3776).
func assertProjectEqual(t *testing.T, want, got *Project) {
	t.Helper()
	if got.Environment != want.Environment {
		t.Errorf("Environment = %q, want %q", got.Environment, want.Environment)
	}
	if got.Source.URL != want.Source.URL || got.Source.Branch != want.Source.Branch {
		t.Errorf("Source = %+v, want %+v", got.Source, want.Source)
	}
	if got.Target.Type != want.Target.Type {
		t.Errorf("Target.Type = %q, want %q", got.Target.Type, want.Target.Type)
	}
	if len(got.Secrets.Files) != len(want.Secrets.Files) {
		t.Errorf("Secrets.Files len = %d, want %d", len(got.Secrets.Files), len(want.Secrets.Files))
	}
	if got.Secrets.Provider != want.Secrets.Provider {
		t.Errorf("Secrets.Provider = %q, want %q", got.Secrets.Provider, want.Secrets.Provider)
	}
}

func TestMarshalProject_OmitsEmptySections(t *testing.T) {
	p := &Project{
		Version:     SchemaVersion,
		Environment: "production",
		Source:      Source{Type: "git", URL: "git@github.com:acme/backend.git", Branch: "main"},
		Target:      Target{Type: TargetCompose, File: DefaultComposeFile},
	}
	ApplyDefaults(p)
	out, err := MarshalProject(p)
	if err != nil {
		t.Fatalf("MarshalProject: %v", err)
	}
	s := string(out)
	// Auth should be absent (ambient) — no empty username/token fields.
	if strings.Contains(s, "auth:") {
		t.Errorf("marshaled output should omit empty auth section; got:\n%s", s)
	}
	if strings.Contains(s, `username: ""`) || strings.Contains(s, `token: ""`) {
		t.Errorf("marshaled output should omit empty auth fields; got:\n%s", s)
	}
}

func TestParse_ServiceOverrides(t *testing.T) {
	yaml := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/backend.git
  branch: main
target:
  type: compose
  file: compose.yaml
  services:
    api:
      env:
        DEBUG: "true"
        LOG_LEVEL: warning
      env_files:
        - /etc/accorda/api.env
        - type: file
          path: /etc/accorda/api-secrets.env
    worker:
      env_files:
        - /etc/accorda/worker.env
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Target.Services) != 2 {
		t.Fatalf("Services = %d entries, want 2", len(p.Target.Services))
	}
	api := p.Target.Services["api"]
	if api.Env["DEBUG"] != "true" || api.Env["LOG_LEVEL"] != "warning" {
		t.Errorf("api.Env = %+v, want DEBUG=true LOG_LEVEL=warning", api.Env)
	}
	if len(api.EnvFiles) != 2 {
		t.Fatalf("api.EnvFiles = %d, want 2", len(api.EnvFiles))
	}
	if api.EnvFiles[0].Path != "/etc/accorda/api.env" {
		t.Errorf("api.EnvFiles[0].Path = %q, want /etc/accorda/api.env", api.EnvFiles[0].Path)
	}
	if api.EnvFiles[1].Path != "/etc/accorda/api-secrets.env" {
		t.Errorf("api.EnvFiles[1].Path = %q, want /etc/accorda/api-secrets.env", api.EnvFiles[1].Path)
	}
	worker := p.Target.Services["worker"]
	if len(worker.EnvFiles) != 1 || worker.EnvFiles[0].Path != "/etc/accorda/worker.env" {
		t.Errorf("worker.EnvFiles = %+v, want one entry /etc/accorda/worker.env", worker.EnvFiles)
	}
}

func TestValidate_ServiceOverrides_EmptyPath(t *testing.T) {
	yaml := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/backend.git
  branch: main
target:
  type: compose
  file: compose.yaml
  services:
    api:
      env_files:
        - ""
`
	_, err := Parse([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "env_files[0]: path is empty") {
		t.Fatalf("Parse error = %v, want empty path validation", err)
	}
}

func TestLoad_ResolvesRelativeEnvFilePaths(t *testing.T) {
	dir := t.TempDir()
	project := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/backend.git
  branch: main
target:
  type: compose
  file: compose.yaml
  services:
    api:
      env_files:
        - secrets/api.env
`
	if err := os.WriteFile(filepath.Join(dir, File), []byte(project), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := p.Target.Services["api"].EnvFiles[0].Path
	want := filepath.Join(dir, "secrets/api.env")
	if got != want {
		t.Errorf("EnvFiles[0].Path = %q, want %q (resolved relative to project dir)", got, want)
	}
}

func TestParse_WebhookNotifications(t *testing.T) {
	yaml := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
notifications:
  webhook: true
  webhooks:
    url: https://hooks.example.com/accorda
    max_retries: 5
    timeout: 2s
    secret: s3cr3t
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !p.Notifications.Webhook {
		t.Error("Notifications.Webhook = false, want true")
	}
	if p.Notifications.WebhookConfig.URL != "https://hooks.example.com/accorda" {
		t.Errorf("URL = %q, want https://hooks.example.com/accorda", p.Notifications.WebhookConfig.URL)
	}
	if p.Notifications.WebhookConfig.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", p.Notifications.WebhookConfig.MaxRetries)
	}
	if p.Notifications.WebhookConfig.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", p.Notifications.WebhookConfig.Timeout)
	}
	if p.Notifications.WebhookConfig.Secret != "s3cr3t" {
		t.Errorf("Secret = %q, want s3cr3t", p.Notifications.WebhookConfig.Secret)
	}
}

func TestParse_WebhookDefaults(t *testing.T) {
	yaml := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
notifications:
  webhook: true
  webhooks:
    url: https://hooks.example.com/accorda
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Notifications.WebhookConfig.MaxRetries != DefaultWebhookMaxRetries {
		t.Errorf("MaxRetries default = %d, want %d", p.Notifications.WebhookConfig.MaxRetries, DefaultWebhookMaxRetries)
	}
	if p.Notifications.WebhookConfig.Timeout != DefaultWebhookTimeout {
		t.Errorf("Timeout default = %v, want %v", p.Notifications.WebhookConfig.Timeout, DefaultWebhookTimeout)
	}
}

func TestValidate_WebhookEnabledWithoutURL(t *testing.T) {
	yaml := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
notifications:
  webhook: true
`
	_, err := Parse([]byte(yaml))
	if err == nil || !strings.Contains(err.Error(), "webhooks.url is empty") {
		t.Fatalf("Parse error = %v, want webhooks.url empty validation", err)
	}
}

func TestValidate_WebhookBlockWithoutEnabled(t *testing.T) {
	p := &Project{
		Version:     1,
		Environment: "production",
		Source:      Source{Type: "git", URL: "git@github.com:acme/infra.git", Branch: "main"},
		Target:      Target{Type: TargetCompose, File: DefaultComposeFile},
		Images:      Images{Pull: PullChanged},
		Reconcile:   Reconcile{Drift: DriftReport},
		Notifications: Notifications{
			Webhook:       false,
			WebhookConfig: &WebhookConfig{URL: "https://hooks.example.com/x"},
		},
	}
	if err := Validate(p); err == nil || !strings.Contains(err.Error(), "notifications.webhook is not enabled") {
		t.Fatalf("Validate error = %v, want webhook-not-enabled validation", err)
	}
}

func TestValidate_WebhookBadScheme(t *testing.T) {
	p := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
notifications:
  webhook: true
  webhooks:
    url: ftp://hooks.example.com/x
`
	_, err := Parse([]byte(p))
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("Parse error = %v, want scheme validation", err)
	}
}

func TestValidate_WebhookNegativeRetries(t *testing.T) {
	p := `version: 1
environment: production
source:
  type: git
  url: git@github.com:acme/infra.git
target:
  type: ` + TargetCompose + `
  file: ` + DefaultComposeFile + `
notifications:
  webhook: true
  webhooks:
    url: https://hooks.example.com/x
    max_retries: -1
`
	_, err := Parse([]byte(p))
	if err == nil || !strings.Contains(err.Error(), "max_retries must be non-negative") {
		t.Fatalf("Parse error = %v, want non-negative retries validation", err)
	}
}
