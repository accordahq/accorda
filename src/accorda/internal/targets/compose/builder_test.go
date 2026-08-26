package compose

import (
	"path/filepath"
	"testing"

	"accorda/internal/config"
	"accorda/internal/targets"
)

func TestResolveComposePaths(t *testing.T) {
	base := t.TempDir()
	managed := filepath.Join(base, config.DefaultComposeFile)
	absolute := filepath.Join(t.TempDir(), config.DefaultComposeFile)
	nested := filepath.Join("deploy", config.DefaultComposeFile)
	sourcePath := func(repositoryPath string) (string, error) {
		return filepath.Join(base, repositoryPath), nil
	}
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
				Project:    config.Project{Target: tc.target, Source: config.Source{Path: tc.sourcePath}},
				SourcePath: sourcePath,
			}
			got, managed, err := resolveComposePaths(ctx)
			if err != nil {
				t.Fatalf("resolveComposePaths(): %v", err)
			}
			if got.Type != tc.want.Type || got.File != tc.want.File || got.Path != tc.want.Path {
				t.Fatalf("resolveComposePaths() = %+v, want %+v", got, tc.want)
			}
			if managed != tc.managed {
				t.Fatalf("resolveComposePaths() managed = %t, want %t", managed, tc.managed)
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
