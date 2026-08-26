package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
	gitSource "accorda/internal/sources/git"
)

func TestRun_DoctorReportsMissingProject(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"doctor", "--dir", t.TempDir()}, &out, &out)
	if !errors.Is(err, errDoctorFailed) {
		t.Fatalf("run(doctor) error = %v, want errDoctorFailed", err)
	}
	if got := out.String(); !strings.Contains(got, "FAIL  Project configuration: config:") {
		t.Fatalf("doctor output = %q, want project configuration failure", got)
	}
	if strings.Contains(out.String(), "not yet implemented") {
		t.Fatalf("doctor output still reports stub: %q", out.String())
	}
}

func TestDiagnoseReportsUnsupportedTarget(t *testing.T) {
	dir := t.TempDir()
	project := `version: 1
environment: production
source:
  type: git
  url: https://git.example.test/acme/app.git
  branch: main
target:
  type: ` + config.TargetKubernetes + `
  path: deploy
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(project), 0o644); err != nil {
		t.Fatalf("write project: %v", err)
	}

	results := diagnose(t.Context(), dir)
	wantResults := []struct {
		status string
		name   string
		detail string
	}{
		{doctorPass, doctorProject, ""},
		{doctorPass, doctorSource, ""},
		{doctorFail, doctorTarget, "not implemented"},
		{doctorInfo, doctorCheckout, ""},
	}
	if len(results) != len(wantResults) {
		t.Fatalf("diagnose() returned %d results, want %d", len(results), len(wantResults))
	}
	for i, want := range wantResults {
		if results[i].status != want.status || results[i].name != want.name {
			t.Fatalf("diagnose()[%d] = %+v, want status %q name %q", i, results[i], want.status, want.name)
		}
		if want.detail != "" && !strings.Contains(results[i].detail, want.detail) {
			t.Fatalf("diagnose()[%d].detail = %q, want containing %q", i, results[i].detail, want.detail)
		}
	}
}

func TestDiagnoseRejectsInvalidExplicitSSHKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "invalid.key")
	if err := os.WriteFile(keyPath, []byte("not a private key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	project := `version: 1
environment: production
source:
  type: git
  url: ssh://git@git.example.test/acme/app.git
  branch: main
  auth:
    type: ssh
    key: ` + keyPath + `
target:
  type: compose
  file: compose.yaml
`
	if err := os.WriteFile(filepath.Join(dir, config.File), []byte(project), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	results := diagnose(t.Context(), dir)
	if len(results) < 2 || results[1].status != doctorFail || !strings.Contains(results[1].detail, "parse auth.key") {
		t.Fatalf("diagnose() source result = %+v, want invalid-key failure", results)
	}
	if strings.Contains(results[1].detail, "not a private key") {
		t.Fatalf("diagnose() leaked key material: %q", results[1].detail)
	}
}

func TestManagedTargetPendingOnlyBeforeCheckoutExists(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "checkout")
	src := gitSource.New(
		config.Source{URL: "https://example.com/acme/repo.git", Branch: "main"},
		gitSource.WithCacheDir(cacheDir),
	)
	missing := fmt.Errorf("compose missing: %w", os.ErrNotExist)
	target := config.Target{Type: config.TargetCompose, File: config.DefaultComposeFile}
	if !managedTargetPending(src, target, missing) {
		t.Fatal("missing unfetched checkout should be pending")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("create invalid checkout: %v", err)
	}
	if managedTargetPending(src, target, missing) {
		t.Fatal("missing Compose file in existing or invalid checkout must fail validation")
	}
}

func TestWriteDoctorReport(t *testing.T) {
	cases := []struct {
		name       string
		results    []doctorResult
		wantOutput string
		wantFailed bool
	}{
		{
			name: "all checks pass",
			results: []doctorResult{
				{name: doctorProject, status: doctorPass},
				{name: doctorSource, status: doctorPass},
				{name: doctorTarget, status: doctorPass},
			},
			wantOutput: "PASS  Project configuration\n" +
				"PASS  Git source configuration\n" +
				"PASS  Deployment target and Docker\n" +
				"Accorda is ready.\n",
		},
		{
			name: "failure includes detail",
			results: []doctorResult{
				{name: doctorProject, status: doctorPass},
				{name: doctorTarget, status: doctorFail, detail: "daemon unavailable"},
			},
			wantOutput: "PASS  Project configuration\n" +
				"FAIL  Deployment target and Docker: daemon unavailable\n",
			wantFailed: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writeDoctorReport(&out, tc.results)
			if got := out.String(); got != tc.wantOutput {
				t.Fatalf("writeDoctorReport() = %q, want %q", got, tc.wantOutput)
			}
			if got := doctorFailed(tc.results); got != tc.wantFailed {
				t.Fatalf("doctorFailed() = %v, want %v", got, tc.wantFailed)
			}
		})
	}
}

func TestDoctorCheck(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantDetail string
	}{
		{name: "pass", wantStatus: doctorPass},
		{name: "fail", err: errors.New("broken"), wantStatus: doctorFail, wantDetail: "broken"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := doctorCheck("check", tc.err)
			if got.status != tc.wantStatus || got.detail != tc.wantDetail {
				t.Fatalf("doctorCheck() = %+v, want status %q detail %q", got, tc.wantStatus, tc.wantDetail)
			}
		})
	}
}
