package targets

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"accorda/internal/config"
)

// TargetContext carries the shared context a target builder needs to
// construct a driver from an Accorda project's configuration. Each builder
// pulls only the fields its driver needs; the struct is the common currency
// so the registry does not switch on target type.
type TargetContext struct {
	// Project is the resolved Accorda project configuration.
	Project config.Project
	// Dir is the operator project directory (the directory containing
	// accorda.yaml).
	Dir string
	// Name is the operator-chosen project name in an ensemble document, or
	// empty for a standalone project. It doubles as the Compose project name
	// override and the image target's service name.
	Name string
	// SourcePath resolves a repository-relative target artifact (for example
	// a Compose file) against the Git source's managed checkout. It returns
	// the absolute path inside the checkout. Drivers that do not consume a
	// file from the checkout (for example the image target) ignore it.
	SourcePath func(repositoryPath string) (string, error)
	// Managed reports whether the target's primary artifact is resolved
	// inside the Git source's managed checkout (true) or is an absolute
	// operator-local override (false). Compose uses it to decide whether to
	// derive a project name from the project directory.
	Managed bool
}

// TargetBuilder constructs a concrete Target from a TargetContext. Each
// driver package registers one builder for its target type via RegisterBuilder
// so the command layer builds targets without importing concrete drivers or
// switching on config.Target.Type.
type TargetBuilder interface {
	// Build returns a configured Target ready for Validate.
	Build(ctx TargetContext) (Target, error)
	// LockIdentityFromConfig returns the stable, target-scoped identity used
	// to key the deployment lock, derived from the raw config.Target without
	// constructing the driver. The lock is acquired before the target is built
	// (docs/DECISIONS.md #44), so the identity must be computable from config
	// alone. dir is the project directory, used to resolve relative target
	// paths into the identity.
	LockIdentityFromConfig(dir string, target config.Target) string
}

// BuilderFunc is the function form of TargetBuilder. The lock-identity
// function is optional; when nil, BuildTarget falls back to a config-derived
// identity (target type plus configured path).
type BuilderFunc struct {
	BuildFn          func(ctx TargetContext) (Target, error)
	LockIdentityFunc func(dir string, target config.Target) string
}

// Build implements TargetBuilder.
func (f BuilderFunc) Build(ctx TargetContext) (Target, error) { return f.BuildFn(ctx) }

// LockIdentityFromConfig implements TargetBuilder.
func (f BuilderFunc) LockIdentityFromConfig(dir string, target config.Target) string {
	if f.LockIdentityFunc != nil {
		return f.LockIdentityFunc(dir, target)
	}
	return defaultLockIdentity(dir, target)
}

// defaultLockIdentity is the fallback identity when a builder does not supply
// one: the target type plus the configured file/path, resolved against the
// project directory for relative paths.
func defaultLockIdentity(dir string, target config.Target) string {
	resolved := target
	if target.File != "" && !filepath.IsAbs(target.File) {
		resolved.File = filepath.Join(dir, target.File)
	} else if target.Path != "" && !filepath.IsAbs(target.Path) {
		resolved.Path = filepath.Join(dir, target.Path)
	}
	return resolved.Type + "\x00" + resolved.ConfiguredPath()
}

var (
	buildersMu sync.RWMutex
	builders   = map[string]TargetBuilder{}
)

// RegisterBuilder registers the builder for a target type. It is called by
// each driver package's init so the registry is populated by import time.
// Registering the same type twice panics, which catches a duplicate
// registration at startup rather than silently shadowing a driver.
func RegisterBuilder(targetType string, b TargetBuilder) {
	buildersMu.Lock()
	defer buildersMu.Unlock()
	if _, dup := builders[targetType]; dup {
		panic(fmt.Sprintf("targets: duplicate builder for %q", targetType))
	}
	builders[targetType] = b
}

// BuildTarget constructs the target for ctx.Project.Target.Type by dispatching
// to its registered builder. It returns a clear error when no builder is
// registered for the type, so an unimplemented target type (kubernetes, helm)
// surfaces as "not implemented" rather than a nil target.
func BuildTarget(ctx TargetContext) (Target, error) {
	buildersMu.RLock()
	b, ok := builders[ctx.Project.Target.Type]
	buildersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("target type %q is not implemented", ctx.Project.Target.Type)
	}
	return b.Build(ctx)
}

// LockIdentityFromConfig returns the stable, target-scoped identity used to
// key the deployment lock, derived from the raw config.Target without
// constructing the driver. It dispatches to the registered builder's
// LockIdentityFromConfig method so the command layer does not switch on
// target type. When no builder is registered for the type, it falls back to
// the default identity (target type plus configured path).
func LockIdentityFromConfig(dir string, target config.Target) string {
	buildersMu.RLock()
	b, ok := builders[target.Type]
	buildersMu.RUnlock()
	if !ok {
		return defaultLockIdentity(dir, target)
	}
	return b.LockIdentityFromConfig(dir, target)
}

// RegisteredTargetTypes returns the target types with registered builders, in
// sorted order. It is intended for diagnostics and tests.
func RegisteredTargetTypes() []string {
	buildersMu.RLock()
	defer buildersMu.RUnlock()
	out := make([]string, 0, len(builders))
	for t := range builders {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
