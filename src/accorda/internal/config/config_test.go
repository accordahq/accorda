package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
  type: compose
  file: compose.yaml
sync:
  interval: 30s
images:
  pull: changed
reconcile:
  drift: repair
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
  type: compose
  path: deploy/compose.yaml
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
  type: kubernetes
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
	if p.Target.Type != "compose" {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, "compose")
	}
	if p.Target.File != "compose.yaml" {
		t.Errorf("Target.File = %q, want %q", p.Target.File, "compose.yaml")
	}
	if p.Sync.Interval != 30*time.Second {
		t.Errorf("Sync.Interval = %v, want %v", p.Sync.Interval, 30*time.Second)
	}
	if p.Images.Pull != "changed" {
		t.Errorf("Images.Pull = %q, want %q", p.Images.Pull, "changed")
	}
	if p.Reconcile.Drift != "repair" {
		t.Errorf("Reconcile.Drift = %q, want %q", p.Reconcile.Drift, "repair")
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
	if p.Target.Type != "compose" {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, "compose")
	}
	if p.Target.Path != "deploy/compose.yaml" {
		t.Errorf("Target.Path = %q, want %q", p.Target.Path, "deploy/compose.yaml")
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
	if p.Images.Pull != "changed" {
		t.Errorf("Images.Pull default = %q, want %q", p.Images.Pull, "changed")
	}
}

func TestParse_KubernetesExample25(t *testing.T) {
	p, err := Parse([]byte(kubernetesExample25))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if p.Target.Type != "kubernetes" {
		t.Errorf("Target.Type = %q, want %q", p.Target.Type, "kubernetes")
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
			yaml: "environment: production\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\n",
			want: "version is required",
		},
		{
			name: "unsupported version",
			yaml: "version: 2\nenvironment: production\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\n",
			want: "version 2 is not supported",
		},
		{
			name: "missing environment",
			yaml: "version: 1\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\n",
			want: "environment is required",
		},
		{
			name: "missing source url",
			yaml: "version: 1\nenvironment: production\ntarget: {type: compose, file: c.yaml}\n",
			want: "source.url is required",
		},
		{
			name: "unsupported source type",
			yaml: "version: 1\nenvironment: production\nsource: {type: s3, url: x}\ntarget: {type: compose, file: c.yaml}\n",
			want: "source.type \"s3\" is not supported",
		},
		{
			name: "missing target type",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\n",
			want: "target.type is required",
		},
		{
			name: "compose without file or path",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: compose}\n",
			want: "target.file or target.path is required for \"compose\" targets",
		},
		{
			name: "kubernetes without path",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: kubernetes}\n",
			want: "target.path is required for \"kubernetes\" targets",
		},
		{
			name: "unsupported target type",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: nomad}\n",
			want: "target.type \"nomad\" is not supported",
		},
		{
			name: "invalid image pull",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\nimages: {pull: sometimes}\n",
			want: "images.pull \"sometimes\" is not valid",
		},
		{
			name: "invalid drift",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\nreconcile: {drift: fix}\n",
			want: "reconcile.drift \"fix\" is not valid",
		},
		{
			name: "secrets empty file entry",
			yaml: "version: 1\nenvironment: production\nsource: {url: x}\ntarget: {type: compose, file: c.yaml}\nsecrets: [\"\"]\n",
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
  type: compose
  file: compose.yaml
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
	if p.Images.Pull != "changed" {
		t.Errorf("Images.Pull default = %q, want %q", p.Images.Pull, "changed")
	}
	if p.Reconcile.Drift != "report" {
		t.Errorf("Reconcile.Drift default = %q, want %q", p.Reconcile.Drift, "report")
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
    type: ssh
    key: /etc/Accorda/git.key
target:
  type: compose
  file: compose.yaml
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
    type: https
    token: ghp_secrettoken
    username: x-access-token
target:
  type: compose
  file: compose.yaml
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
    type: ssh
target:
  type: compose
  file: compose.yaml
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
    type: https
target:
  type: compose
  file: compose.yaml
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
  type: compose
  file: compose.yaml
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
  type: compose
  file: compose.yaml
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
  type: compose
  file: compose.yaml
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse([]byte(c.yaml))
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
			// The round-tripped project must match the original on the fields
			// that matter for reconciliation.
			if p2.Environment != p.Environment {
				t.Errorf("Environment = %q, want %q", p2.Environment, p.Environment)
			}
			if p2.Source.URL != p.Source.URL || p2.Source.Branch != p.Source.Branch {
				t.Errorf("Source = %+v, want %+v", p2.Source, p.Source)
			}
			if p2.Target.Type != p.Target.Type {
				t.Errorf("Target.Type = %q, want %q", p2.Target.Type, p.Target.Type)
			}
			if len(p2.Secrets.Files) != len(p.Secrets.Files) {
				t.Errorf("Secrets.Files len = %d, want %d", len(p2.Secrets.Files), len(p.Secrets.Files))
			}
			if p2.Secrets.Provider != p.Secrets.Provider {
				t.Errorf("Secrets.Provider = %q, want %q", p2.Secrets.Provider, p.Secrets.Provider)
			}
		})
	}
}

func TestMarshalProject_OmitsEmptySections(t *testing.T) {
	p := &Project{
		Version:     SchemaVersion,
		Environment: "production",
		Source:      Source{Type: "git", URL: "git@github.com:acme/backend.git", Branch: "main"},
		Target:      Target{Type: TargetCompose, File: "compose.yaml"},
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
