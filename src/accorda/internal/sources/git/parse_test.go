package git

import (
	"path/filepath"
	"strings"
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

func TestComposePathRejectsCheckoutEscape(t *testing.T) {
	for _, input := range []string{"../compose.yaml", "/tmp/compose.yaml"} {
		if _, err := ComposePath(input, ""); err == nil {
			t.Errorf("ComposePath(%q, \"\") expected error", input)
		}
	}
}

func TestCheckoutPath(t *testing.T) {
	base := t.TempDir()
	g := New(config.Source{URL: "https://example.com/acme/repo.git"}, WithBaseDir(base))
	got, err := g.CheckoutPath("deploy/compose.yaml")
	if err != nil {
		t.Fatalf("CheckoutPath: %v", err)
	}
	wantDir, err := g.cacheDir()
	if err != nil {
		t.Fatalf("cacheDir: %v", err)
	}
	want := filepath.Join(wantDir, "deploy", "compose.yaml")
	if got != want {
		t.Errorf("CheckoutPath() = %q, want %q", got, want)
	}
}

func TestCheckoutDir(t *testing.T) {
	base := t.TempDir()
	g := New(config.Source{URL: "https://example.com/acme/repo.git"}, WithBaseDir(base))
	got, err := g.CheckoutDir()
	if err != nil {
		t.Fatalf("CheckoutDir: %v", err)
	}
	wantDir, err := g.cacheDir()
	if err != nil {
		t.Fatalf("cacheDir: %v", err)
	}
	if got != wantDir {
		t.Errorf("CheckoutDir() = %q, want %q", got, wantDir)
	}
}

func TestCheckoutDir_NilReceiver(t *testing.T) {
	var g *Git
	if _, err := g.CheckoutDir(); err == nil {
		t.Fatal("CheckoutDir() on nil receiver expected error")
	}
}

func TestRepoDirName(t *testing.T) {
	cases := []string{
		"git@github.com:acme/infra.git",
		"ssh://git@git.internal/acme/prod.git",
		"https://git.internal/acme/prod",
		"https://user:token@git.internal/acme/prod",
		"",
	}
	for _, input := range cases {
		got := repoDirName(input)
		if !strings.HasPrefix(got, "accorda-") || len(got) != len("accorda-")+64 {
			t.Errorf("repoDirName(%q) = %q, want accorda- plus SHA-256 hex", input, got)
		}
	}
	clean := repoDirName("https://git.internal/acme/prod")
	credentialed := repoDirName("https://user:token@git.internal/acme/prod.git/")
	if clean != credentialed {
		t.Errorf("canonical repository identities differ: %q != %q", clean, credentialed)
	}
}

func TestCanonicalRepositoryURL_InvalidURLFallsBackSafely(t *testing.T) {
	const invalid = "https://git.internal/%zz"
	if got := canonicalRepositoryURL(invalid); got != invalid {
		t.Fatalf("canonicalRepositoryURL() = %q, want original invalid URL", got)
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
		if got := isComposeFile(c.in); got != c.want {
			t.Errorf("isComposeFile(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
