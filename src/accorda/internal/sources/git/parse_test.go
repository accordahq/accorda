package git

import (
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
)

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
