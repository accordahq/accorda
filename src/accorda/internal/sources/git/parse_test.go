package git

import (
	"testing"

	"accorda/internal/config"
)

func TestServicesPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", config.DefaultComposeFile},
		{"deploy/", "deploy/" + config.DefaultComposeFile},
		{"deploy", "deploy/" + config.DefaultComposeFile},
		{"deploy/" + config.DefaultComposeFile, "deploy/" + config.DefaultComposeFile},
		{"deploy/docker-compose.yml", "deploy/docker-compose.yml"},
		{"deploy/services", "deploy/services/" + config.DefaultComposeFile},
	}
	for _, c := range cases {
		if got := servicesPath(c.in); got != c.want {
			t.Errorf("servicesPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRepoDirName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"git@github.com:acme/infra.git", "accorda-github.com-acme-infra"},
		{"ssh://git@git.internal/acme/prod.git", "accorda-git.internal-acme-prod"},
		{"https://git.internal/acme/prod", "accorda-git.internal-acme-prod"},
		{"https://user:token@git.internal/acme/prod", "accorda-git.internal-acme-prod"},
		{"", "accorda-accorda-repo"},
	}
	for _, c := range cases {
		if got := repoDirName(c.in); got != c.want {
			t.Errorf("repoDirName(%q) = %q, want %q", c.in, got, c.want)
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
		{"app.yaml", false},
		{"Makefile", false},
	}
	for _, c := range cases {
		if got := isComposeFile(c.in); got != c.want {
			t.Errorf("isComposeFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
