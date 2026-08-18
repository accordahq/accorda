package git

import (
	"testing"
)

func TestServicesPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", defaultComposeFile},
		{"deploy/", "deploy/" + defaultComposeFile},
		{"deploy", "deploy/" + defaultComposeFile},
		{"deploy/" + defaultComposeFile, "deploy/" + defaultComposeFile},
		{"deploy/docker-compose.yml", "deploy/docker-compose.yml"},
		{"deploy/services", "deploy/services/" + defaultComposeFile},
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
		{"compose.yaml", true},
		{"compose.yml", true},
		{"docker-compose.yaml", true},
		{"docker-compose.yml", true},
		{"deploy/compose.yaml", true},
		{"app.yaml", false},
		{"Makefile", false},
	}
	for _, c := range cases {
		if got := isComposeFile(c.in); got != c.want {
			t.Errorf("isComposeFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
