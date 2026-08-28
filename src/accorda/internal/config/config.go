package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the canonical name of the Accorda project file on disk.
const File = "accorda.yaml"

// DefaultComposeFile is the Compose file used when a project or Git source
// does not configure a different services file.
const DefaultComposeFile = "compose.yaml"

// SchemaVersion is the only format version currently accepted by the loader.
const SchemaVersion = 1

// Valid image pull policies (docs/ACCORDA.md §9).
const (
	PullChanged = "changed"
	PullMissing = "missing"
	PullAlways  = "always"
	PullNever   = "never"
)

// projectNameFirstChar is the set of characters allowed as the first
// character of an ensemble project name, mirroring the Compose project-name
// charset so a name is always a safe Compose project name and a safe path
// segment (docs/ACCORDA.md §49).
const projectNameFirstChar = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// projectNameChar is the set of characters allowed in an ensemble project
// name after the first, mirroring the Compose project-name charset.
const projectNameChar = projectNameFirstChar + "_-"

// Valid drift repair policies (docs/ACCORDA.md §5, §47).
const (
	DriftRepair   = "repair"
	DriftReport   = "report"
	DriftDisabled = "disabled"
)

// Valid source types (docs/ACCORDA.md §13). Git is the only source type; it
// has two modes selected by which field is configured:
//
//   - url reconciles from a remote Git repository cloned into the private
//     cache (internal/sources/git, remote mode).
//   - path reconciles in place from a user-owned local git worktree without
//     cloning (internal/sources/git, in-place mode).
const (
	SourceGit = "git"
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
	TargetImage      = "image"
)

// Project is the unified representation of one Accorda project configuration.
//
// Field ordering and yaml tags mirror the layout shown in docs/ACCORDA.md
// §25 (Unified Project Format). All nested structs are pointers so that
// "not set" is distinguishable from "set to zero value" during validation.
//
// Name is optional and meaningful only inside an Ensemble (docs/ACCORDA.md
// §49): a multi-project accorda.yaml groups several Projects under a
// projects: list, each with an operator-chosen name (api, worker, monitoring,
// ...) so one agent can manage several workloads. A standalone project leaves
// Name empty.
type Project struct {
	Name        string `yaml:"name,omitempty"`
	Version     int    `yaml:"version"`
	Environment string `yaml:"environment"`
	Source      Source `yaml:"source"`
	// Target is the legacy single-target alias (docs/ACCORDA.md §25). It is
	// accepted for backwards compatibility and promoted into Targets during
	// ApplyDefaults/applyDefaults; new configuration should use Targets.
	Target Target `yaml:"target,omitempty"`
	// Targets is the list of deployment targets reconciled from the single
	// Source. A project may declare several targets (Compose + Compose, or
	// future Compose + Kubernetes) so one repository revision fans out to
	// each deployment artifact (issue #103, docs/DECISIONS.md #53).
	Targets       Targets       `yaml:"targets,omitempty"`
	Sync          Sync          `yaml:"sync,omitempty"`
	Images        Images        `yaml:"images,omitempty"`
	Reconcile     Reconcile     `yaml:"reconcile,omitempty"`
	Health        Health        `yaml:"health,omitempty"`
	Secrets       Secrets       `yaml:"secrets,omitempty"`
	Notifications Notifications `yaml:"notifications,omitempty"`
}

// Ensemble is the multi-project configuration for one agent
// (docs/ACCORDA.md §49). It groups several independent Projects under a
// single accorda.yaml so one agent reconciles several Compose projects,
// repositories, and environments concurrently. Each Project is reconciled
// independently: it has its own source, target, receipt journal, and
// target-scoped lock, so a failure or deployment in one workload cannot
// block or mutate another.
//
// The document root carries the settings that are shared by all members
// (docs/DECISIONS.md #43): the schema Version, the global Sync cadence, and
// the global Images, Reconcile, and Health defaults. Version and Sync are
// global and not overridable — one agent runs on one schema and one polling
// cadence. Images, Reconcile, and Health act as defaults that each member may
// override; per-member overrides are resolved into the member's concrete
// Project at parse time.
//
// A document is an Ensemble exactly when it has a top-level projects: list;
// otherwise it is a single Project. ParseDocument and LoadDocument dispatch
// between the two shapes.
type Ensemble struct {
	Version   int       `yaml:"version"`
	Sync      Sync      `yaml:"sync,omitempty"`
	Images    Images    `yaml:"images,omitempty"`
	Reconcile Reconcile `yaml:"reconcile,omitempty"`
	Health    Health    `yaml:"health,omitempty"`
	Projects  []Project `yaml:"projects"`
}

// ensembleMember is the per-workload YAML shape inside a projects: list. It is
// deliberately narrower than Project: it rejects the global fields (version,
// sync) that live at the Ensemble root so an operator cannot silently declare
// a per-member schema version or polling cadence that would be ignored or
// diverge from the single agent loop (docs/DECISIONS.md #43). Images,
// Reconcile, and Health are optional pointers so "unset" is distinguishable
// from "set to zero value" when merging the global default.
type ensembleMember struct {
	Name          string        `yaml:"name,omitempty"`
	Environment   string        `yaml:"environment"`
	Source        Source        `yaml:"source"`
	Target        Target        `yaml:"target,omitempty"`
	Targets       Targets       `yaml:"targets,omitempty"`
	Images        *Images       `yaml:"images,omitempty"`
	Reconcile     *Reconcile    `yaml:"reconcile,omitempty"`
	Health        *Health       `yaml:"health,omitempty"`
	Secrets       Secrets       `yaml:"secrets,omitempty"`
	Notifications Notifications `yaml:"notifications,omitempty"`
}

// resolveMembers turns raw ensemble members into concrete Projects by merging
// the Ensemble root's global defaults into each member (docs/DECISIONS.md
// #48). The schema version and the sync interval are global and not
// overridable, so every member inherits them verbatim; Images, Reconcile, and
// Health fall back to the root value only when the member does not override
// them. Reconcile, which has more than one field, is merged field-by-field so
// a member overriding only drift retains the root remove_orphans default.
func (e *Ensemble) resolveMembers(members []ensembleMember) {
	e.Projects = make([]Project, len(members))
	for i, m := range members {
		e.Projects[i] = e.resolveMember(m)
	}
}

// resolveMember turns one raw ensemble member into a concrete Project by
// merging the Ensemble root's global defaults into it (docs/DECISIONS.md
// #48). The schema version and the sync interval are global and not
// overridable, so every member inherits them verbatim; Images, Reconcile,
// and Health fall back to the root value only when the member does not
// override them. Reconcile, which has more than one field, is merged
// field-by-field so a member overriding only drift retains the root
// remove_orphans default.
func (e *Ensemble) resolveMember(m ensembleMember) Project {
	p := Project{
		Name:          m.Name,
		Version:       e.Version,
		Environment:   m.Environment,
		Source:        m.Source,
		Target:        m.Target,
		Targets:       m.Targets,
		Secrets:       m.Secrets,
		Notifications: m.Notifications,
	}
	// The polling cadence is global and non-overridable.
	p.Sync.Interval = e.Sync.Interval
	p.Images = resolveValue(m.Images, e.Images)
	p.Reconcile = e.resolveReconcile(m.Reconcile)
	p.Health = resolveValue(m.Health, e.Health)
	return p
}

// resolveValue returns the member's override when set, otherwise the root
// default. It is generic over Images and Health, both of which are value
// types resolved by a simple nil-pointer check.
func resolveValue[T any](override *T, root T) T {
	if override != nil {
		return *override
	}
	return root
}

// resolveReconcile merges a member's Reconcile override into the root default
// field-by-field (docs/DECISIONS.md #43). A member overriding only drift must
// not silently drop the root's remove_orphans default.
func (e *Ensemble) resolveReconcile(override *Reconcile) Reconcile {
	if override == nil {
		return e.Reconcile
	}
	merged := e.Reconcile
	if override.Drift != "" {
		merged.Drift = override.Drift
	}
	if override.RemoveOrphans != nil {
		merged.RemoveOrphans = override.RemoveOrphans
	}
	return merged
}

// Source describes the Git source to reconcile from (docs/ACCORDA.md §13).
type Source struct {
	Type   string `yaml:"type"`
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
	Path   string `yaml:"path,omitempty"`
	Auth   Auth   `yaml:"auth,omitempty"`
}

// Auth describes how the Git source authenticates to its remote
// (docs/ACCORDA.md §13, §15). The Type selects the interpretation of the
// remaining fields:
//
//   - ssh:   Auth.Key is a filesystem path to a private key. The Git adapter
//     reads and parses it for go-git's SSH transport; key material is never
//     logged.
//   - https: Auth.Token is a personal access token or installation token used
//     as go-git HTTP basic authentication. Auth.Username is optional and
//     defaults to the user embedded in the URL or "oauth2" for token auth.
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
	Key string `yaml:"key,omitempty"`
	// Username is the HTTPS username. For token auth this is often
	// "oauth2" or "x-access-token"; it defaults accordingly.
	Username string `yaml:"username,omitempty"`
	// Token is the HTTPS credential/token. It is treated as a secret and
	// must never be logged.
	Token string `yaml:"token,omitempty"`
}

// Target describes the deployment target (docs/ACCORDA.md §8, §24, §25).
// It is intentionally target-agnostic: the Type selects the interpretation of
// the remaining fields. Compose targets require a File; Kubernetes targets
// require a Path.
type Target struct {
	// Name is an optional, operator-chosen identifier for this target. It is
	// used for per-target attribution in output, receipt journals, and locks.
	// When omitted, an Identity is derived deterministically from the target's
	// type and configured path/image, so multi-target projects without names
	// still get stable, collision-free per-target identity (issue #103,
	// docs/DECISIONS.md #53).
	Name string `yaml:"name,omitempty"`
	Type string `yaml:"type"`
	Path string `yaml:"path,omitempty"`
	File string `yaml:"file,omitempty"`
	// Services holds per-service environment overrides applied at deploy time
	// (docs/DECISIONS.md #23). They do not enter desired state, hashing, or
	// receipts; they are merged into the deploy Compose file's environment:
	// before `docker compose up -d` runs.
	Services map[string]ServiceOverride `yaml:"services,omitempty"`
	// Image is the single container image reference for a raw image target
	// (target.type: image, docs/DECISIONS.md #24). It is the desired image the
	// image driver reconciles; no Compose file is parsed.
	Image string `yaml:"image,omitempty"`
	// Env is the inline environment for a raw image target, keyed by variable
	// name (docs/DECISIONS.md #24). It is the per-image analog of the Compose
	// ServiceOverride.env and enters desired state because, unlike Compose
	// env_files, it is Git-authored config rather than operator-local secret
	// material.
	Env map[string]string `yaml:"env,omitempty"`
	// Ports are the published port mappings for a raw image target
	// (docs/DECISIONS.md #24), in the Docker "host:container" short form.
	Ports []string `yaml:"ports,omitempty"`
}

// ConfiguredPath returns the target's configured file or path, preferring
// File over Path (docs/ACCORDA.md §8, §25). It is the single source of truth
// for "what did the operator configure as the target artifact?" so callers do
// not each re-implement the File-else-Path fallback.
func (t Target) ConfiguredPath() string {
	if t.File != "" {
		return t.File
	}
	return t.Path
}

// Identity returns a stable, human-readable identifier for this target: the
// operator-chosen Name when set, otherwise a deterministic label derived from
// the type and configured path/image (for example "compose:docker-compose.yml"
// or "image:registry.example.com/edge:1"). It is the single source of truth
// for per-target attribution, so output headers, state-transition events, and
// receipt keys all agree on how a target is identified — and, unlike the
// internal lock key, it never embeds a NUL separator (issue #103,
// docs/DECISIONS.md #53).
func (t Target) Identity() string {
	if t.Name != "" {
		return t.Name
	}
	if t.Image != "" {
		return t.Type + ":" + t.Image
	}
	return t.Type + ":" + t.ConfiguredPath()
}

// Targets is the list of deployment targets a project reconciles from one
// source (issue #103, docs/DECISIONS.md #53). It is a plain slice so the
// strict loader (KnownFields) checks each entry's fields; the legacy single
// target: shorthand is promoted into a one-element Targets list by
// ApplyDefaults.
type Targets []Target

// Empty reports whether the target list is unset (nil or empty). It is used
// by ApplyDefaults to decide whether to promote the legacy single Target.
func (ts Targets) Empty() bool {
	return len(ts) == 0
}

// PromoteTarget promotes the legacy single Target into the Targets list when
// the plural list is unset (docs/DECISIONS.md #53). It is the single source
// of truth for the backwards-compatible `target:` shorthand so callers (and
// the CLI) always read Targets after normalization.
func (p *Project) PromoteTarget() {
	if !p.Targets.Empty() {
		return
	}
	if p.Target.Type != "" {
		p.Targets = Targets{p.Target}
		return
	}
	// No target configured; leave an empty list so ValidateTargets reports it.
	p.Targets = Targets{}
}

// NormalizedTargets returns the project's effective target list after
// promoting the legacy single Target, without mutating the project. It lets
// read-only consumers iterate Targets even before ApplyDefaults has run.
func (p *Project) NormalizedTargets() Targets {
	if !p.Targets.Empty() {
		return p.Targets
	}
	if p.Target.Type != "" {
		return Targets{p.Target}
	}
	return Targets{}
}

// ServiceOverride declares per-service environment inputs applied at deploy
// time (docs/DECISIONS.md #23). Both fields are optional and combinable;
// inline env values take precedence over env_files entries on key collision.
type ServiceOverride struct {
	// Env is inline key/value environment variables merged into the
	// service's environment: at deploy time, overriding and adding to
	// values declared in the Compose file.
	Env map[string]string `yaml:"env,omitempty"`
	// EnvFiles is a list of local .env files whose entries are read at
	// deploy time and merged into the service's environment:. Each entry
	// is a plain string path (short form) or a mapping with type: file and
	// path: (long form). Paths are operator-local and never committed to
	// Git; their contents are never stored in desired state or receipts.
	EnvFiles []EnvFileRef `yaml:"env_files,omitempty"`
}

// EnvFileRef is one entry in a service's env_files list. It accepts a short
// form (a plain string path) and a long form (a mapping with type and path),
// mirroring Compose's own env_file syntax.
type EnvFileRef struct {
	Path string
}

// UnmarshalYAML implements yaml.Unmarshaler so EnvFileRef accepts both the
// short form (a plain string) and the long form (a mapping with type: file
// and path:).
func (e *EnvFileRef) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		return value.Decode(&e.Path)
	case yaml.MappingNode:
		for i := 0; i < len(value.Content); i += 2 {
			key := value.Content[i]
			val := value.Content[i+1]
			switch key.Value {
			case "path":
				if err := val.Decode(&e.Path); err != nil {
					return fmt.Errorf("env_files: path: %w", err)
				}
			case "type":
				// Accepted for future extensibility; currently only "file"
				// is meaningful and the value is not stored.
			default:
				return fmt.Errorf("env_files: unknown field %q", key.Value)
			}
		}
		return nil
	default:
		return fmt.Errorf("env_files: expected a string or mapping, got %s", value.Tag)
	}
}

// Sync controls the reconciliation cadence (docs/ACCORDA.md §45, §47).
type Sync struct {
	Interval time.Duration `yaml:"interval,omitempty"`
}

// Images controls the image pull policy (docs/ACCORDA.md §9).
type Images struct {
	Pull string `yaml:"pull,omitempty"`
}

// Reconcile controls drift handling and orphan removal
// (docs/ACCORDA.md §5, §47).
type Reconcile struct {
	Drift         string `yaml:"drift,omitempty"`
	RemoveOrphans *bool  `yaml:"remove_orphans,omitempty"`
}

// Health controls health verification (docs/ACCORDA.md §19).
type Health struct {
	Timeout time.Duration `yaml:"timeout,omitempty"`
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

// MarshalYAML implements yaml.Marshaler so Secrets round-trips through the
// strict loader. UnmarshalYAML accepts two shapes (a list of file paths or a
// {provider: ...} mapping); MarshalYAML emits the same shape that was decoded
// so a Project produced by Parse round-trips through MarshalProject back to a
// document the strict loader accepts (docs/ACCORDA.md §25).
//
// When both Files and Provider are empty, MarshalYAML returns a nil node so
// the field is omitted entirely by the parent's omitempty tag.
func (s Secrets) MarshalYAML() (any, error) {
	if len(s.Files) > 0 {
		return s.Files, nil
	}
	if s.Provider != "" {
		return map[string]string{"provider": s.Provider}, nil
	}
	return nil, nil
}

// Notifications enables notification channels (docs/ACCORDA.md §21, §39).
// The generic webhook channel (§21) is configured through WebhookConfig; the
// remaining channels are booleans that gate future adapters.
type Notifications struct {
	GitHub  bool `yaml:"github,omitempty"`
	Webhook bool `yaml:"webhook,omitempty"`
	Slack   bool `yaml:"slack,omitempty"`
	Discord bool `yaml:"discord,omitempty"`
	Ntfy    bool `yaml:"ntfy,omitempty"`
	// WebhookConfig configures the generic outbound webhook consumer
	// (docs/ACCORDA.md §21). It is honored only when Webhook is true; a
	// non-empty WebhookConfig without Webhook: true is a configuration error
	// surfaced by Validate so a stale block does not silently enable delivery.
	// It is a pointer so the marshaled document omits the block entirely when
	// unset.
	WebhookConfig *WebhookConfig `yaml:"webhooks,omitempty"`
}

// WebhookConfig configures the generic outbound webhook notification target
// (docs/ACCORDA.md §21). The consumer subscribes to the event bus and POSTs
// each event as a JSON payload to URL, retrying transient failures up to
// MaxRetries times with exponential backoff. Secret values in the payload are
// redacted before serialization so credentials never leave the process.
type WebhookConfig struct {
	// URL is the outbound webhook endpoint. It must be an absolute https (or
	// http for local testing) URL. Required when the webhook channel is
	// enabled.
	URL string `yaml:"url"`
	// MaxRetries is the maximum number of retry attempts after the initial
	// request fails. It defaults to DefaultWebhookMaxRetries when zero. A
	// negative value is rejected by Validate.
	MaxRetries int `yaml:"max_retries,omitempty"`
	// Timeout is the per-request timeout for each webhook delivery. It
	// defaults to DefaultWebhookTimeout when zero.
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// Secret is an optional shared secret sent as the X-Accorda-Signature
	// header (HMAC-SHA256 of the payload body) so the receiver can verify
	// authenticity. It is treated as a secret and never logged or included in
	// the payload.
	Secret string `yaml:"secret,omitempty"`
}

// DefaultWebhookMaxRetries is the default retry count for the webhook
// consumer when WebhookConfig.MaxRetries is unset (docs/ACCORDA.md §21).
const DefaultWebhookMaxRetries = 3

// DefaultWebhookTimeout is the default per-request timeout for the webhook
// consumer when WebhookConfig.Timeout is unset (docs/ACCORDA.md §21).
const DefaultWebhookTimeout = 10 * time.Second

// Load reads the Accorda project file from dir and returns the validated
// configuration. The project file must be named File (accorda.yaml).
func Load(dir string) (*Project, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	project, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := validateCredentialFileMode(path, project); err != nil {
		return nil, err
	}
	resolveServiceOverridesPaths(project, dir)
	return project, nil
}

// LoadDocument reads the accorda.yaml document from dir and returns either a
// single Project or a multi-project Ensemble (docs/ACCORDA.md §25, §49). It is
// the dispatcher used by CLI commands so they can serve both the single-project
// and the multi-project shape without knowing which they read. For the Ensemble
// shape, per-project env_files paths are resolved relative to dir, and each
// project's credential-file mode is checked against the same shared file.
func LoadDocument(dir string) (*Document, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	doc, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	if doc.Project != nil {
		if err := validateCredentialFileMode(path, doc.Project); err != nil {
			return nil, err
		}
		resolveServiceOverridesPaths(doc.Project, dir)
		return doc, nil
	}
	for i := range doc.Ensemble.Projects {
		p := &doc.Ensemble.Projects[i]
		if err := validateCredentialFileMode(path, p); err != nil {
			return nil, err
		}
		resolveServiceOverridesPaths(p, dir)
	}
	return doc, nil
}

// resolveServiceOverridesPaths resolves env_file paths for a single project,
// mirroring the per-project resolution in Load.
func resolveServiceOverridesPaths(p *Project, dir string) {
	targets := p.NormalizedTargets()
	for i := range targets {
		resolveServiceOverridePaths(targets[i].Services, dir)
	}
}

// resolveServiceOverridePaths resolves env_files paths relative to the
// project directory so operators can use relative paths in accorda.yaml
// (docs/DECISIONS.md #23). Absolute paths are left unchanged.
func resolveServiceOverridePaths(services map[string]ServiceOverride, dir string) {
	for name, svc := range services {
		for i, f := range svc.EnvFiles {
			if filepath.IsAbs(f.Path) {
				continue
			}
			svc.EnvFiles[i].Path = filepath.Join(dir, f.Path)
		}
		services[name] = svc
	}
}

func validateCredentialFileMode(path string, project *Project) error {
	if project == nil || !hasInlineCredential(project.Source) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("config: inspect permissions: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("config: %q contains credentials and must have permissions 0600 or stricter", File)
	}
	return nil
}

func hasInlineCredential(source Source) bool {
	if strings.TrimSpace(source.Auth.Token) != "" {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(source.URL))
	if err != nil || parsed.User == nil {
		return false
	}
	_, hasPassword := parsed.User.Password()
	return hasPassword
}

// Document is either a single Project or a multi-project Ensemble decoded
// from an accorda.yaml file (docs/ACCORDA.md §25, §49). In the Ensemble shape,
// the schema version, sync cadence, and policy defaults are resolved into
// each member's concrete Project at parse time (docs/DECISIONS.md #43).
type Document struct {
	// Project is the single-project document when the file has no top-level
	// projects: list; otherwise it is nil.
	Project *Project
	// Ensemble is the multi-project document when the file has a top-level
	// projects: list; otherwise it is nil. Its members are already resolved
	// with the document-root globals applied and per-member overrides merged.
	Ensemble *Ensemble
}

// ParseDocument decodes an accorda.yaml document and returns either a single
// Project or a multi-project Ensemble (docs/ACCORDA.md §25, §49). A top-level
// projects: list selects the Ensemble shape; its absence selects the single
// Project shape. Both shapes are validated strictly and receive their
// defaults. An empty document decodes to a zero Project, which Validate
// rejects with a clear error rather than silently treating it as valid.
func ParseDocument(data []byte) (*Document, error) {
	var root yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", File, err)
	}
	if hasEnsembleProjects(&root) {
		return parseEnsemble(data)
	}
	// Single project: re-decode through the strict loader so the full
	// document shape is validated.
	p, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return &Document{Project: p}, nil
}

// hasEnsembleProjects reports whether the decoded document has a top-level
// projects: list. It inspects the mapping keys without applying strict
// decoding, so a single-project document that lacks a projects key falls
// through to the single-Project shape regardless of its other fields.
func hasEnsembleProjects(root *yaml.Node) bool {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return false
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "projects" {
			return true
		}
	}
	return false
}

// parseEnsemble decodes and validates a multi-project document
// (docs/ACCORDA.md §49). It decodes the Ensemble root (whose globals —
// version, sync, images, reconcile, health — live beside the projects: list)
// alongside strict per-member entries, then merges the globals into each
// member so every Project carries the concrete effective values the CLI and
// targets consume (docs/DECISIONS.md #43). The members are decoded through
// ensembleMember, which rejects the per-member version and sync blocks, so
// those globals cannot be silently duplicated or diverge per workload.
func parseEnsemble(data []byte) (*Document, error) {
	var doc struct {
		Version   int              `yaml:"version"`
		Sync      Sync             `yaml:"sync,omitempty"`
		Images    Images           `yaml:"images,omitempty"`
		Reconcile Reconcile        `yaml:"reconcile,omitempty"`
		Health    Health           `yaml:"health,omitempty"`
		Projects  []ensembleMember `yaml:"projects"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", File, err)
	}
	e := &Ensemble{
		Version:   doc.Version,
		Sync:      doc.Sync,
		Images:    doc.Images,
		Reconcile: doc.Reconcile,
		Health:    doc.Health,
	}
	e.applyEnsembleDefaults()
	e.resolveMembers(doc.Projects)
	for i := range e.Projects {
		applyDefaults(&e.Projects[i])
	}
	if err := ValidateEnsemble(e); err != nil {
		return nil, err
	}
	return &Document{Ensemble: e}, nil
}

// applyEnsembleDefaults fills in the document-root globals that the spec
// considers optional but which have a defined default behavior, mirroring
// applyDefaults for the single-project shape (docs/ACCORDA.md §9, §19, §45).
// They are applied to the root before resolveMembers so every member inherits
// the same effective default.
func (e *Ensemble) applyEnsembleDefaults() {
	if e.Sync.Interval == 0 {
		e.Sync.Interval = 30 * time.Second
	}
	if e.Images.Pull == "" {
		e.Images.Pull = PullChanged
	}
	if e.Reconcile.Drift == "" {
		e.Reconcile.Drift = DriftReport
	}
	if e.Health.Timeout == 0 {
		e.Health.Timeout = 120 * time.Second
	}
}

// ValidateEnsemble validates a multi-project document (docs/ACCORDA.md §49).
// The document must declare a schema version and at least one project. Every
// project must be a valid Project, and project names must be unique,
// non-empty, and confined to the Compose project-name charset so they are
// safe as a Compose project name and a filesystem path segment. Uniqueness
// is compared case-insensitively (Compose project names normalize to
// lowercase), so `API` and `api` are rejected as colliding — two ensemble
// members with the same effective Compose project name would otherwise make
// `--remove-orphans` destructive across projects. It returns the first error
// encountered.
func ValidateEnsemble(e *Ensemble) error {
	if e == nil {
		return errors.New("config: ensemble is nil")
	}
	if err := validateSchemaVersion(e.Version); err != nil {
		return err
	}
	if len(e.Projects) == 0 {
		return errors.New("config: projects: at least one project is required")
	}
	seen := make(map[string]struct{}, len(e.Projects))
	for i := range e.Projects {
		p := &e.Projects[i]
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("config: projects[%d].name is required", i)
		}
		if err := validateProjectName(name); err != nil {
			return fmt.Errorf("config: projects[%d].%w", i, err)
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("config: project name %q collides with another project after normalization", name)
		}
		seen[key] = struct{}{}
		if err := Validate(p); err != nil {
			return err
		}
	}
	return nil
}

// validateProjectName checks that name is a safe Compose project name and
// filesystem path segment: non-empty, first character alphanumeric, remaining
// characters in [a-zA-Z0-9_-]. This prevents path traversal (e.g. `..`, `/`)
// and invalid Compose project names from reaching the target or state paths
// (docs/ACCORDA.md §49; matches compose-go NormalizeProjectName).
func validateProjectName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !strings.ContainsRune(projectNameFirstChar, rune(name[0])) {
		return fmt.Errorf("name %q must start with an alphanumeric character", name)
	}
	for _, r := range name[1:] {
		if !strings.ContainsRune(projectNameChar, r) {
			return fmt.Errorf("name %q contains invalid character %q (allowed: alphanumeric, underscore, hyphen)", name, r)
		}
	}
	return nil
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

// MarshalProject encodes a Project as the canonical accorda.yaml document
// (docs/ACCORDA.md §25). It is the inverse of Parse for the fields init
// writes: a Project produced by Parse round-trips through MarshalProject back
// to an equivalent document the strict loader accepts, including the Secrets
// list/provider shapes. Optional sections that are at their zero value are
// omitted so the output is minimal and matches what a user would author by
// hand.
//
// MarshalProject does not validate p; callers that need a valid document
// (for example `accorda init`) should construct a Project the loader would
// accept. The marshaled document uses the same yaml tags as Parse, so the
// strict loader round-trips it.
func MarshalProject(p *Project) ([]byte, error) {
	if p == nil {
		return nil, errors.New("config: project is nil")
	}
	// Emit the canonical targets: list form (issue #103, docs/DECISIONS.md
	// #53). The legacy single target: is promoted into Targets by
	// ApplyDefaults; marshaling a copy with Target cleared avoids emitting
	// both forms, keeping init output minimal and matching the documented
	// unified project format.
	outProject := *p
	outProject.Targets = p.NormalizedTargets()
	outProject.Target = Target{}
	out, err := yaml.Marshal(outProject)
	if err != nil {
		return nil, fmt.Errorf("config: marshal %q: %w", File, err)
	}
	return out, nil
}

// applyDefaults fills in values that the spec considers optional but which
// have a defined default behavior. Defaults are only applied when the field
// is at its zero value, so an explicit zero (for example `interval: 0s`) is
// indistinguishable from omission and receives the default.
func applyDefaults(p *Project) {
	if p.Source.Type == "" {
		p.Source.Type = "git"
	}
	p.PromoteTarget()
	if p.Source.Branch == "" {
		// In-place sources derive the effective branch from worktree HEAD; this
		// remote-mode default remains in normalized config but is ignored.
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
	if p.Notifications.WebhookConfig != nil {
		applyWebhookDefaults(p.Notifications.WebhookConfig)
	}
}

// applyWebhookDefaults fills in the webhook consumer's optional fields with
// their defined defaults (docs/ACCORDA.md §21). Defaults are only applied
// when the field is at its zero value.
func applyWebhookDefaults(w *WebhookConfig) {
	if w.MaxRetries == 0 {
		w.MaxRetries = DefaultWebhookMaxRetries
	}
	if w.Timeout == 0 {
		w.Timeout = DefaultWebhookTimeout
	}
}

// ApplyDefaults fills in optional fields with their defined default values.
// It is exported so callers that construct a Project directly (for example
// `accorda init`) can produce a project the loader would accept before
// validating and marshaling it. It is the same logic Parse runs after
// decoding.
func ApplyDefaults(p *Project) {
	applyDefaults(p)
}

// Validate reports the first concrete configuration error, with a field-oriented
// message suitable for surfacing to the user. It delegates each section to a
// focused helper so the top-level function stays small and each rule is easy
// to reason about in isolation.
func Validate(p *Project) error {
	if p == nil {
		return errors.New("config: project is nil")
	}
	if err := validateVersion(p); err != nil {
		return err
	}
	if err := validateSource(p); err != nil {
		return err
	}
	if err := ValidateTargets(p); err != nil {
		return err
	}
	if err := validateImages(p); err != nil {
		return err
	}
	if err := validateReconcile(p); err != nil {
		return err
	}
	if err := validateHealthSync(p); err != nil {
		return err
	}
	if err := validateNotifications(p); err != nil {
		return err
	}
	return validateSecrets(p)
}

// validateVersion checks the schema version and environment fields.
func validateVersion(p *Project) error {
	if err := validateSchemaVersion(p.Version); err != nil {
		return err
	}
	if strings.TrimSpace(p.Environment) == "" {
		return errors.New("config: environment is required")
	}
	return nil
}

// validateSchemaVersion checks a document schema version (single-project
// Project or ensemble root) is present and supported.
func validateSchemaVersion(version int) error {
	if version == 0 {
		return errors.New("config: version is required")
	}
	if version != SchemaVersion {
		return fmt.Errorf("config: version %d is not supported (want %d)", version, SchemaVersion)
	}
	return nil
}

// validateSource checks the source fields and delegates auth to
// validateSourceAuth. A git source has two modes selected by whether a URL is
// configured: url reconciles from a remote repository cloned into the cache
// (path is then optional and points at a compose file or directory within the
// repo); with no url, path reconciles in place from a local worktree. Exactly
// one source of repository content must be available: url or (in local mode)
// path.
func validateSource(p *Project) error {
	if p.Source.Type != SourceGit {
		return fmt.Errorf("config: source.type %q is not supported (want %q)", p.Source.Type, SourceGit)
	}
	if p.Source.URL != "" {
		// Remote mode: path is optional (a repo-relative compose path).
		if strings.TrimSpace(p.Source.Branch) == "" {
			return errors.New("config: source.branch is required")
		}
		return validateSourceAuth(p)
	}
	// In-place mode: no URL, so a local worktree path is required.
	if strings.TrimSpace(p.Source.Path) == "" {
		return errors.New("config: source.url or source.path is required")
	}
	return nil
}

// validateSourceAuth checks the source auth configuration
// (docs/ACCORDA.md §13, §15). An empty auth.type means "use the ambient Git
// environment" and is always valid.
func validateSourceAuth(p *Project) error {
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
	return nil
}

// ValidateTargets checks the project's target list (docs/DECISIONS.md #53).
// A project must declare at least one target, each target must be valid, and
// target identities must be unique within the project so two targets that
// would share a receipt journal or deployment lock are rejected.
func ValidateTargets(p *Project) error {
	targets := p.NormalizedTargets()
	if len(targets) == 0 {
		return errors.New("config: at least one target is required")
	}
	seen := make(map[string]struct{}, len(targets))
	for i := range targets {
		tgt := &targets[i]
		if err := validateTarget(tgt); err != nil {
			return fmt.Errorf("config: targets[%d]: %w", i, err)
		}
		identity := tgt.ConfiguredPath()
		if _, dup := seen[identity]; dup {
			return fmt.Errorf("config: targets[%d] with path %q collides with another target (target identities must be unique within a project)", i, identity)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

// validateTarget checks one deployment target's type and required fields. It
// is called per entry by ValidateTargets; keeping it unexported avoids a
// speculative public API until an individual-target selection surface (for
// example `accorda plan --target`) actually exists.
func validateTarget(tgt *Target) error {
	if tgt.Type == "" {
		return errors.New("target.type is required")
	}
	switch tgt.Type {
	case TargetCompose:
		// The compose file may be given via "file" (§8 example) or "path"
		// (§25 example); at least one is required.
		if tgt.File == "" && tgt.Path == "" {
			return fmt.Errorf("target.file or target.path is required for %q targets", TargetCompose)
		}
	case TargetKubernetes, TargetHelm:
		if tgt.Path == "" {
			return fmt.Errorf("target.path is required for %q targets", tgt.Type)
		}
	case TargetImage:
		if strings.TrimSpace(tgt.Image) == "" {
			return fmt.Errorf("target.image is required for %q targets", TargetImage)
		}
		for _, port := range tgt.Ports {
			if strings.TrimSpace(port) == "" {
				return fmt.Errorf("target.ports: empty entry is not allowed")
			}
		}
	default:
		return fmt.Errorf("target.type %q is not supported", tgt.Type)
	}
	return validateServiceOverrides(tgt.Services)
}

// validateServiceOverrides checks the per-service env override entries
// (docs/DECISIONS.md #23). Each service name must be non-empty, and each
// env_files path must be non-empty.
func validateServiceOverrides(services map[string]ServiceOverride) error {
	for name, svc := range services {
		if strings.TrimSpace(name) == "" {
			return errors.New("config: target.services: service name is empty")
		}
		for i, f := range svc.EnvFiles {
			if strings.TrimSpace(f.Path) == "" {
				return fmt.Errorf("config: target.services.%s.env_files[%d]: path is empty", name, i)
			}
		}
	}
	return nil
}

// validateImages checks the image pull policy (docs/ACCORDA.md §9).
func validateImages(p *Project) error {
	switch p.Images.Pull {
	case PullChanged, PullMissing, PullAlways, PullNever:
		return nil
	default:
		return fmt.Errorf("config: images.pull %q is not valid (want one of %s)", p.Images.Pull,
			strings.Join([]string{PullChanged, PullMissing, PullAlways, PullNever}, ", "))
	}
}

// validateReconcile checks the drift repair policy (docs/ACCORDA.md §5, §47).
func validateReconcile(p *Project) error {
	switch p.Reconcile.Drift {
	case DriftRepair, DriftReport, DriftDisabled:
		return nil
	default:
		return fmt.Errorf("config: reconcile.drift %q is not valid (want one of %s)", p.Reconcile.Drift,
			strings.Join([]string{DriftRepair, DriftReport, DriftDisabled}, ", "))
	}
}

// validateHealthSync checks the health timeout and sync interval are
// non-negative.
func validateHealthSync(p *Project) error {
	if p.Health.Timeout < 0 {
		return errors.New("config: health.timeout must be non-negative")
	}
	if p.Sync.Interval < 0 {
		return errors.New("config: sync.interval must be non-negative")
	}
	return nil
}

// validateSecrets checks the secrets list form. The list form (files) and the
// provider form are structurally distinct YAML shapes, so they cannot both
// appear in one document; only the list form needs per-entry validation.
func validateSecrets(p *Project) error {
	for i, f := range p.Secrets.Files {
		if strings.TrimSpace(f) == "" {
			return fmt.Errorf("config: secrets.files[%d] is empty", i)
		}
	}
	return nil
}

// validateNotifications checks the notification channel configuration
// (docs/ACCORDA.md §21). The generic webhook channel requires a URL and is
// rejected when enabled without one, or when a webhook block is present
// without enabling the channel so a stale block does not silently enable
// delivery.
func validateNotifications(p *Project) error {
	w := p.Notifications.WebhookConfig
	if w == nil {
		if !p.Notifications.Webhook {
			return nil
		}
		return errors.New("config: notifications.webhook is enabled but webhooks.url is empty")
	}
	hasBlock := w.URL != "" || w.MaxRetries != 0 || w.Timeout != 0 || w.Secret != ""
	if !p.Notifications.Webhook {
		if hasBlock {
			return errors.New("config: notifications.webhooks is set but notifications.webhook is not enabled")
		}
		return nil
	}
	if strings.TrimSpace(w.URL) == "" {
		return errors.New("config: notifications.webhook is enabled but webhooks.url is empty")
	}
	u, err := url.Parse(w.URL)
	if err != nil {
		return fmt.Errorf("config: notifications.webhooks.url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: notifications.webhooks.url scheme %q is not supported (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("config: notifications.webhooks.url is missing a host")
	}
	if w.MaxRetries < 0 {
		return errors.New("config: notifications.webhooks.max_retries must be non-negative")
	}
	if w.Timeout < 0 {
		return errors.New("config: notifications.webhooks.timeout must be non-negative")
	}
	return nil
}
