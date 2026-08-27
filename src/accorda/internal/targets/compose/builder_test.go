package compose

import (
	"os"
	"path/filepath"
	"testing"

	"accorda/internal/config"
	"accorda/internal/targets"
)

type testWorktree struct {
	root    string
	binding string
}

func (w testWorktree) CheckoutDir() (string, error) { return w.root, nil }
func (w testWorktree) CheckoutPath(repositoryPath string) (string, error) {
	return filepath.Join(w.root, filepath.FromSlash(repositoryPath)), nil
}
func (w testWorktree) BindingPath() string { return w.binding }

func TestResolveComposePaths(t *testing.T) {
	base := t.TempDir()
	managed := filepath.Join(base, config.DefaultComposeFile)
	absolute := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	nested := filepath.Join("deploy", config.DefaultComposeFile)
	cases := []struct {
		name       string
		target     config.Target
		sourcePath string
		want       config.Target
		managed    bool
	}{
		{
			name:       "relative file",
			target:     config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile},
			sourcePath: config.DefaultComposeFile,
			want:       config.Target{Type: config.TargetCompose, File: managed},
			managed:    true,
		},
		{
			name:       "relative path",
			target:     config.Target{Type: config.TargetCompose, Path: nested},
			sourcePath: "deploy/" + config.DefaultComposeFile,
			want:       config.Target{Type: config.TargetCompose, File: filepath.Join(base, "deploy", config.DefaultComposeFile)},
			managed:    true,
		},
		{
			name:   "absolute file",
			target: config.Target{Type: config.TargetCompose, File: absolute},
			want:   config.Target{Type: config.TargetCompose, File: absolute},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := targets.TargetContext{
				Project:  config.Project{Target: tc.target, Source: config.Source{URL: "https://example.com/repo.git", Path: tc.sourcePath}},
				Worktree: testWorktree{root: base, binding: tc.sourcePath},
			}
			got, artifact, managed, err := resolveComposePaths(ctx)
			if err != nil {
				t.Fatalf("resolveComposePaths(): %v", err)
			}
			if got.Type != tc.want.Type || got.File != tc.want.File || got.Path != tc.want.Path {
				t.Fatalf("resolveComposePaths() = %+v, want %+v", got, tc.want)
			}
			if managed != tc.managed {
				t.Fatalf("resolveComposePaths() managed = %t, want %t", managed, tc.managed)
			}
			if artifact != tc.sourcePath && tc.managed {
				t.Fatalf("resolveComposePaths() artifact = %q, want %q", artifact, tc.sourcePath)
			}
		})
	}
}

func TestComposeArtifactInPlaceBinding(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "deploy")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(nested, config.DefaultComposeFile)
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	cases := []struct {
		name       string
		binding    string
		configured string
		want       string
	}{
		{name: "worktree", binding: root, configured: "deploy/compose.yaml", want: "deploy/compose.yaml"},
		{name: "explicit file", binding: file, configured: "ignored.yaml", want: "deploy/compose.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := targets.TargetContext{
				Project: config.Project{
					Source: config.Source{Path: tc.binding},
					Target: config.Target{Type: config.TargetCompose, File: tc.configured},
				},
				Worktree: testWorktree{root: root, binding: tc.binding},
			}
			got, err := composeArtifact(ctx, tc.configured)
			if err != nil {
				t.Fatalf("composeArtifact: %v", err)
			}
			if got != tc.want {
				t.Fatalf("composeArtifact = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLockIdentityFromConfig(t *testing.T) {
	dir := t.TempDir()
	t1 := lockIdentityFromConfig(dir, config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile})
	t2 := lockIdentityFromConfig(dir, config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile})
	if t1 != t2 {
		t.Errorf("same target produced different identities: %q vs %q", t1, t2)
	}
	if t1 == "" {
		t.Error("lock identity is empty")
	}
}
