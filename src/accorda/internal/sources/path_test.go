package sources

import (
	"testing"

	"accorda/internal/config"
)

func TestComposePath(t *testing.T) {
	cases := []struct {
		source, target, want string
	}{
		{"", "", config.DefaultComposeFile},
		{"", "docker-compose.yml", "docker-compose.yml"},
		{"deploy/", "", "deploy/" + config.DefaultComposeFile},
		{"deploy", "docker-compose.yml", "deploy/docker-compose.yml"},
		{"deploy/" + config.DefaultComposeFile, "other.yml", "deploy/" + config.DefaultComposeFile},
		{"deploy/custom.yaml", config.DefaultComposeFile, "deploy/custom.yaml"},
		{"deploy/services", "", "deploy/services/" + config.DefaultComposeFile},
	}
	for _, c := range cases {
		got, err := ComposePath(c.source, c.target)
		if err != nil {
			t.Fatalf("ComposePath(%q, %q): %v", c.source, c.target, err)
		}
		if got != c.want {
			t.Errorf("ComposePath(%q, %q) = %q, want %q", c.source, c.target, got, c.want)
		}
	}
}

func TestComposePathRejectsWorktreeEscape(t *testing.T) {
	for _, input := range []string{"../compose.yaml", "/tmp/compose.yaml"} {
		if _, err := ComposePath(input, ""); err == nil {
			t.Errorf("ComposePath(%q, \"\") expected error", input)
		}
	}
}

func TestIsComposeFile(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{config.DefaultComposeFile, true},
		{"compose.yml", true},
		{"docker-compose.yaml", true},
		{"docker-compose.yml", true},
		{"deploy/" + config.DefaultComposeFile, true},
		{"app.yaml", true},
		{"Makefile", false},
	}
	for _, c := range cases {
		if got := IsComposeFile(c.in); got != c.want {
			t.Errorf("IsComposeFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCleanRepositoryPathRejectsEscape(t *testing.T) {
	for _, input := range []string{"../compose.yaml", "/tmp/compose.yaml", "", "."} {
		if _, err := CleanRepositoryPath(input); err == nil {
			t.Errorf("CleanRepositoryPath(%q) expected error", input)
		}
	}
}

func TestCleanRepositoryPath(t *testing.T) {
	if got, err := CleanRepositoryPath("deploy/compose.yaml"); err != nil || got != "deploy/compose.yaml" {
		t.Errorf("CleanRepositoryPath() = %q, %v; want deploy/compose.yaml, nil", got, err)
	}
}
