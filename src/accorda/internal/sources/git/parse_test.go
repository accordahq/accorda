package git

import (
	"testing"

	"accorda/internal/core/state"
)

func TestParseComposeServices_ImageAndEnv(t *testing.T) {
	data := []byte(`
services:
  api:
    image: ghcr.io/acme/api:1.9
    environment:
      LOG_LEVEL: warning
      DEBUG: "true"
  redis:
    image: redis:8
  postgres:
    image: postgres:17
    environment:
      - POSTGRES_PASSWORD=secret
      - POSTGRES_USER
`)
	services, err := parseComposeServices(data)
	if err != nil {
		t.Fatalf("parseComposeServices: %v", err)
	}
	if len(services) != 3 {
		t.Fatalf("got %d services, want 3: %+v", len(services), services)
	}
	api := services["api"]
	if api.Image != "ghcr.io/acme/api:1.9" {
		t.Errorf("api.Image = %q, want ghcr.io/acme/api:1.9", api.Image)
	}
	if api.Env["LOG_LEVEL"] != "warning" {
		t.Errorf("api.Env[LOG_LEVEL] = %q, want warning", api.Env["LOG_LEVEL"])
	}
	if api.Env["DEBUG"] != "true" {
		t.Errorf("api.Env[DEBUG] = %q, want true", api.Env["DEBUG"])
	}
	pg := services["postgres"]
	if pg.Env["POSTGRES_PASSWORD"] != "secret" {
		t.Errorf("postgres.Env[POSTGRES_PASSWORD] = %q, want secret", pg.Env["POSTGRES_PASSWORD"])
	}
	if _, ok := pg.Env["POSTGRES_USER"]; !ok {
		t.Errorf("postgres.Env missing POSTGRES_USER, got %+v", pg.Env)
	}
	if services["redis"].Image != "redis:8" {
		t.Errorf("redis.Image = %q, want redis:8", services["redis"].Image)
	}
}

func TestParseComposeServices_EmptyAndUnknown(t *testing.T) {
	// Unknown top-level keys are ignored; a service with only unknown fields
	// is dropped because it carries no image or env.
	data := []byte(`version: "3"
services:
  api:
    image: ghcr.io/acme/api:1.9
  helper:
    build: .
    depends_on:
      - api
`)
	services, err := parseComposeServices(data)
	if err != nil {
		t.Fatalf("parseComposeServices: %v", err)
	}
	if _, ok := services["helper"]; ok {
		t.Errorf("helper should be dropped, got %+v", services["helper"])
	}
	if services["api"].Image != "ghcr.io/acme/api:1.9" {
		t.Errorf("api.Image = %q, want ghcr.io/acme/api:1.9", services["api"].Image)
	}
}

func TestParseComposeServices_EmptyInput(t *testing.T) {
	services, err := parseComposeServices(nil)
	if err != nil {
		t.Fatalf("parseComposeServices(nil): %v", err)
	}
	if len(services) != 0 {
		t.Errorf("got %d services, want 0", len(services))
	}
}

func TestParseComposeServices_HashInQuotedValues(t *testing.T) {
	// A `#` inside a quoted value must be preserved; a `#` preceded by
	// whitespace starts a comment. This guards the regression called out in
	// the PR #46 review (MEDIUM).
	data := []byte(`services:
  api:
    image: "ghcr.io/acme/api:1.9#build"
    environment:
      PASSWORD: "a#b"
      TOKEN: 'c#d'
      PLAIN: e#f   # trailing comment
`)
	services, err := parseComposeServices(data)
	if err != nil {
		t.Fatalf("parseComposeServices: %v", err)
	}
	api := services["api"]
	if api.Image != "ghcr.io/acme/api:1.9#build" {
		t.Errorf("api.Image = %q, want ghcr.io/acme/api:1.9#build", api.Image)
	}
	if api.Env["PASSWORD"] != "a#b" {
		t.Errorf("api.Env[PASSWORD] = %q, want a#b", api.Env["PASSWORD"])
	}
	if api.Env["TOKEN"] != "c#d" {
		t.Errorf("api.Env[TOKEN] = %q, want c#d", api.Env["TOKEN"])
	}
	if api.Env["PLAIN"] != "e#f" {
		t.Errorf("api.Env[PLAIN] = %q, want e#f", api.Env["PLAIN"])
	}
}

func TestStripComment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"image: redis:8 # comment", "image: redis:8"},
		{"# full line comment", ""},
		{"PASSWORD: \"a#b\"", "PASSWORD: \"a#b\""},
		{"TOKEN: 'c#d' # trailing", "TOKEN: 'c#d'"},
		{"image: \"x#y\"", "image: \"x#y\""},
		{"PLAIN: e#f", "PLAIN: e#f"},
		{"no comment here", "no comment here"},
	}
	for _, c := range cases {
		if got := stripComment(c.in); got != c.want {
			t.Errorf("stripComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

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

func TestSetEnv(t *testing.T) {
	env := map[string]string{}
	env = setEnv(env, "LOG_LEVEL=info")
	env = setEnv(env, "FEATURE_FLAG")
	if env["LOG_LEVEL"] != "info" {
		t.Errorf("LOG_LEVEL = %q, want info", env["LOG_LEVEL"])
	}
	if env["FEATURE_FLAG"] != "" {
		t.Errorf("FEATURE_FLAG = %q, want empty", env["FEATURE_FLAG"])
	}
}

func TestStateServiceRoundTrip(t *testing.T) {
	// Guard against accidental removal of the Env field plumbing.
	svc := state.Service{Image: "redis:8", Env: map[string]string{"FOO": "bar"}}
	clone := svc.Clone()
	clone.Env["FOO"] = "baz"
	if svc.Env["FOO"] != "bar" {
		t.Errorf("Clone aliased Env: original mutated to %q", svc.Env["FOO"])
	}
}
