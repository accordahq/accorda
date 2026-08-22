package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"accorda/internal/config"
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
	if len(results) != 3 {
		t.Fatalf("diagnose() returned %d results, want 3", len(results))
	}
	if results[0].status != doctorPass || results[1].status != doctorPass {
		t.Fatalf("diagnose() prerequisites = %+v, want both PASS", results[:2])
	}
	if results[2].status != doctorFail || !strings.Contains(results[2].detail, "not implemented") {
		t.Fatalf("diagnose() target result = %+v, want unsupported-target failure", results[2])
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
				{name: doctorCompose, status: doctorPass},
			},
			wantOutput: "PASS  Project configuration\n" +
				"PASS  Git source configuration\n" +
				"PASS  Compose target and Docker\n" +
				"Accorda is ready.\n",
		},
		{
			name: "failure includes detail",
			results: []doctorResult{
				{name: doctorProject, status: doctorPass},
				{name: doctorCompose, status: doctorFail, detail: "daemon unavailable"},
			},
			wantOutput: "PASS  Project configuration\n" +
				"FAIL  Compose target and Docker: daemon unavailable\n",
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
