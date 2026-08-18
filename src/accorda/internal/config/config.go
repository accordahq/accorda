package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the canonical name of the Accorda project file on disk.
const File = "accorda.yaml"

// SchemaVersion is the only format version currently accepted by the loader.
const SchemaVersion = 1

// Valid image pull policies (docs/ACCORDA.md §9).
const (
	PullChanged = "changed"
	PullMissing = "missing"
	PullAlways  = "always"
	PullNever   = "never"
)

// Valid drift repair policies (docs/ACCORDA.md §5, §47).
const (
	DriftRepair   = "repair"
	DriftReport   = "report"
	DriftDisabled = "disabled"
)

// Valid Git source auth types (docs/ACCORDA.md §13, §15).
const (
	AuthSSH   = "ssh"
	AuthHTTPS = "https"
)

// Valid target types (docs/ACCORDA.md §8, §24, §25).
const (
	TargetCompose    = "compose"
	TargetKubernetes = "kubernetes"
	TargetHelm       = "helm"
)

// Project is the decoded and validated Accorda project configuration.
//
// Field ordering and yaml tags mirror the layout shown in docs/ACCORDA.md
// §25 (Unified Project Format). All nested structs are pointers so that
// "not set" is distinguishable from "set to zero value" during validation.
type Project struct {
	Version       int           `yaml:"version"`
	Environment   string        `yaml:"environment"`
	Source        Source        `yaml:"source"`
	Target        Target        `yaml:"target"`
	Sync          Sync          `yaml:"sync"`
	Images        Images        `yaml:"images"`
	Reconcile     Reconcile     `yaml:"reconcile"`
	Health        Health        `yaml:"health"`
	Secrets       Secrets       `yaml:"secrets"`
	Notifications Notifications `yaml:"notifications"`
}

// Source describes the Git source to reconcile from (docs/ACCORDA.md §13).
type Source struct {
	Type   string `yaml:"type"`
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
	Path   string `yaml:"path"`
	Auth   Auth   `yaml:"auth"`
}

// Auth describes how the Git source authenticates to its remote
// (docs/ACCORDA.md §13, §15). The Type selects the interpretation of the
// remaining fields:
//
//   - ssh:   Auth.Key is a filesystem path to a private key. It is surfaced
//     to Git via GIT_SSH_COMMAND; the key material is never read or logged
//     by Accorda.
//   - https: Auth.Token is a personal access token or installation token
//     embedded in the remote URL (or supplied via the Git credential
//     helper environment). Auth.Username is optional and defaults to the
//     user embedded in the URL or "oauth2" for token auth.
//
// When Type is empty, the Git source inherits the user's environment (SSH
// agent, Git credential helpers), which remains the default for local
// development. Secret fields are never logged (docs/ACCORDA.md §18, §56).
type Auth struct {
	// Type is "ssh" or "https" (docs/ACCORDA.md §15). Empty means "use the
	// ambient Git environment" and is the default.
	Type string `yaml:"type"`
	// Key is the path to an SSH private key used when Type == "ssh", e.g.
	// "/etc/Accorda/git.key".
	Key string `yaml:"key"`
	// Username is the HTTPS username. For token auth this is often
	// "oauth2" or "x-access-token"; it defaults accordingly.
	Username string `yaml:"username"`
	// Token is the HTTPS credential/token. It is treated as a secret and
	// must never be logged.
	Token string `yaml:"token"`
}

// Target describes the deployment target (docs/ACCORDA.md §8, §24, §25).
// It is intentionally target-agnostic: the Type selects the interpretation of
// the remaining fields. Compose targets require a File; Kubernetes targets
// require a Path.
type Target struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
	File string `yaml:"file"`
}

// Sync controls the reconciliation cadence (docs/ACCORDA.md §45, §47).
type Sync struct {
	Interval time.Duration `yaml:"interval"`
}

// Images controls the image pull policy (docs/ACCORDA.md §9).
type Images struct {
	Pull string `yaml:"pull"`
}

// Reconcile controls drift handling and orphan removal
// (docs/ACCORDA.md §5, §47).
type Reconcile struct {
	Drift         string `yaml:"drift"`
	RemoveOrphans *bool  `yaml:"remove_orphans"`
}

// Health controls health verification (docs/ACCORDA.md §19).
type Health struct {
	Timeout time.Duration `yaml:"timeout"`
}

// Secrets holds secret references. The spec shows two shapes for this field
// (docs/ACCORDA.md §25): a list of encrypted file paths, or a provider
// descriptor. To keep the loader target-agnostic and strict, both are modeled
// and exactly one representation is expected per document.
//
// The two shapes are:
//
//	secrets:                 # list of encrypted file paths
//	  - deploy/prod.env.sops
//
//	secrets:                 # provider descriptor
//	  provider: sops
//
// UnmarshalYAML accepts either shape and records which representation was
// used so Validate can enforce that they are not mixed.
type Secrets struct {
	// Files is the list form, e.g. ["deploy/prod.env.sops"].
	Files []string
	// Provider is the provider descriptor form, e.g. "sops".
	Provider string
}

// UnmarshalYAML implements yaml.Unmarshaler so Secrets can accept either the
// list form or the provider-descriptor form shown in docs/ACCORDA.md §25.
func (s *Secrets) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.SequenceNode:
		var files []string
		if err := value.Decode(&files); err != nil {
			return fmt.Errorf("secrets: expected a list of strings: %w", err)
		}
		s.Files = files
		return nil
	case yaml.MappingNode:
		// Only the "provider" key is accepted in the mapping form. Unknown
		// keys are rejected to keep configuration strict.
		for i := 0; i < len(value.Content); i += 2 {
			key := value.Content[i]
			val := value.Content[i+1]
			if key.Value != "provider" {
				return fmt.Errorf("secrets: unknown field %q (only \"provider\" is allowed)", key.Value)
			}
			if err := val.Decode(&s.Provider); err != nil {
				return fmt.Errorf("secrets.provider: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("secrets: must be a list of files or a provider mapping, got %s", value.Tag)
	}
}

// Notifications enables notification channels (docs/ACCORDA.md §21, §39).
type Notifications struct {
	GitHub  bool `yaml:"github"`
	Webhook bool `yaml:"webhook"`
	Slack   bool `yaml:"slack"`
	Discord bool `yaml:"discord"`
	Ntfy    bool `yaml:"ntfy"`
}

// Load reads the Accorda project file from dir and returns the validated
// configuration. The project file must be named File (accorda.yaml).
func Load(dir string) (*Project, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates an Accorda project file from raw YAML bytes.
func Parse(data []byte) (*Project, error) {
	var p Project
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true) // reject unknown fields with a clear error
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", File, err)
	}
	applyDefaults(&p)
	if err := Validate(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// applyDefaults fills in values that the spec considers optional but which
// have a defined default behavior. Defaults are only applied when the field
// is at its zero value, so an explicit zero (for example `interval: 0s`) is
// indistinguishable from omission and receives the default.
func applyDefaults(p *Project) {
	if p.Source.Type == "" {
		p.Source.Type = "git"
	}
	if p.Source.Branch == "" {
		p.Source.Branch = "main"
	}
	if p.Images.Pull == "" {
		p.Images.Pull = PullChanged
	}
	if p.Reconcile.Drift == "" {
		p.Reconcile.Drift = DriftReport
	}
	if p.Health.Timeout == 0 {
		p.Health.Timeout = 120 * time.Second
	}
	if p.Sync.Interval == 0 {
		p.Sync.Interval = 30 * time.Second
	}
}

// Validate reports the first concrete configuration error, with a field-oriented
// message suitable for surfacing to the user.
func Validate(p *Project) error {
	if p == nil {
		return errors.New("config: project is nil")
	}
	if p.Version == 0 {
		return errors.New("config: version is required")
	}
	if p.Version != SchemaVersion {
		return fmt.Errorf("config: version %d is not supported (want %d)", p.Version, SchemaVersion)
	}
	if strings.TrimSpace(p.Environment) == "" {
		return errors.New("config: environment is required")
	}

	// Source
	if p.Source.Type == "" {
		return errors.New("config: source.type is required")
	}
	if p.Source.Type != "git" {
		return fmt.Errorf("config: source.type %q is not supported (want %q)", p.Source.Type, "git")
	}
	if p.Source.URL == "" {
		return errors.New("config: source.url is required")
	}
	if strings.TrimSpace(p.Source.Branch) == "" {
		return errors.New("config: source.branch is required")
	}

	// Source auth (docs/ACCORDA.md §13, §15). An empty auth.type means
	// "use the ambient Git environment" and is always valid.
	switch p.Source.Auth.Type {
	case "":
		// No explicit auth; inherit the user's environment.
	case AuthSSH:
		if strings.TrimSpace(p.Source.Auth.Key) == "" {
			return errors.New("config: source.auth.key is required when auth.type is \"ssh\"")
		}
	case AuthHTTPS:
		if strings.TrimSpace(p.Source.Auth.Token) == "" {
			return errors.New("config: source.auth.token is required when auth.type is \"https\"")
		}
	default:
		return fmt.Errorf("config: source.auth.type %q is not supported (want %q or %q)",
			p.Source.Auth.Type, AuthSSH, AuthHTTPS)
	}

	// Target
	if p.Target.Type == "" {
		return errors.New("config: target.type is required")
	}
	switch p.Target.Type {
	case TargetCompose:
		// The compose file may be given via "file" (§8 example) or "path"
		// (§25 example); at least one is required.
		if p.Target.File == "" && p.Target.Path == "" {
			return fmt.Errorf("config: target.file or target.path is required for %q targets", TargetCompose)
		}
	case TargetKubernetes, TargetHelm:
		if p.Target.Path == "" {
			return fmt.Errorf("config: target.path is required for %q targets", p.Target.Type)
		}
	default:
		return fmt.Errorf("config: target.type %q is not supported", p.Target.Type)
	}

	// Images
	switch p.Images.Pull {
	case PullChanged, PullMissing, PullAlways, PullNever:
	default:
		return fmt.Errorf("config: images.pull %q is not valid (want one of %s)", p.Images.Pull,
			strings.Join([]string{PullChanged, PullMissing, PullAlways, PullNever}, ", "))
	}

	// Reconcile
	switch p.Reconcile.Drift {
	case DriftRepair, DriftReport, DriftDisabled:
	default:
		return fmt.Errorf("config: reconcile.drift %q is not valid (want one of %s)", p.Reconcile.Drift,
			strings.Join([]string{DriftRepair, DriftReport, DriftDisabled}, ", "))
	}

	// Health
	if p.Health.Timeout < 0 {
		return errors.New("config: health.timeout must be non-negative")
	}

	// Sync
	if p.Sync.Interval < 0 {
		return errors.New("config: sync.interval must be non-negative")
	}

	// Secrets: either the list form (files) or the provider form is
	// accepted; the two are structurally distinct YAML shapes, so they
	// cannot both appear in one document.
	if len(p.Secrets.Files) > 0 {
		for i, f := range p.Secrets.Files {
			if strings.TrimSpace(f) == "" {
				return fmt.Errorf("config: secrets.files[%d] is empty", i)
			}
		}
	}

	return nil
}
