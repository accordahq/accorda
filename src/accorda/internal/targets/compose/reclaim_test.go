package compose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"accorda/internal/config"
	"accorda/internal/core/plan"
)

// fakeDockerCLI is a test double for the dockerCli seam. It records every
// `docker` invocation and returns a canned error to exercise failure paths.
type fakeDockerCLI struct {
	calls [][]string
	err   error
}

func (f *fakeDockerCLI) Run(_ context.Context, args ...string) error {
	f.calls = append(f.calls, args)
	return f.err
}

// reclaimHarness builds a Compose target with a fake Docker client (a stale
// container with the given labels + project) and a fake docker CLI, all wired
// through a real deploy file declaring the services.
func reclaimHarness(t *testing.T, staleLabels map[string]string, staleProject string) (*Target, *fakeDockerClient, *fakeDockerCLI) {
	t.Helper()
	path := writeSourceCompose(t, `services:
  db:
    image: postgres:17
    container_name: sonarqube_db
`)
	cli := &fakeDockerClient{
		containers: []container.Summary{
			{
				ID:     "stale-id",
				Names:  []string{"/sonarqube_db"},
				Labels: staleLabels,
			},
		},
		inspected: map[string]container.InspectResponse{
			"stale-id": {
				Config: &container.Config{
					Image:  "postgres:17",
					Labels: staleLabels,
				},
				Mounts: []container.MountPoint{
					{Type: "volume", Name: staleProject + "_postgresql_data"},
				},
			},
		},
	}
	dcli := &fakeDockerCLI{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path},
		WithDockerClient(cli), WithDockerCLI(dcli), WithProjectName("dev-sonar"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tgt, cli, dcli
}

// A stale container owned by Accorda (accorda.managed=true) from a different
// project must be reclaimed, and its named volume migrated to the current
// project namespace.
func TestReclaimStaleContainers_RemovesOwnedStaleAndMigratesVolume(t *testing.T) {
	tgt, _, dcli := reclaimHarness(t,
		map[string]string{
			composeProjectLabel: "local-developer-sonar",
			accordaManagedLabel: "true",
			composeServiceLabel: "db",
		},
		"local-developer-sonar",
	)

	err := tgt.reclaimStaleContainers(context.Background(), tgt.file, []string{"db"})
	if err != nil {
		t.Fatalf("reclaimStaleContainers: %v", err)
	}

	// Volume migration copy must target the current project namespace.
	if len(dcli.calls) < 1 {
		t.Fatalf("docker cli calls = %v, want a volume copy + rm", dcli.calls)
	}
	volCall := dcli.calls[0]
	if !hasVolumes(volCall, "local-developer-sonar_postgresql_data", "dev-sonar_postgresql_data") {
		t.Errorf("volume migration call = %v, want copy from old to new project volume", volCall)
	}
	// The stale container must be force-removed by name.
	if !hasRm(dcli.calls, "sonarqube_db") {
		t.Errorf("docker cli calls = %v, want docker rm -f sonarqube_db", dcli.calls)
	}
}

// A container that does NOT carry the Accorda ownership label must never be
// removed, even if it collides by container_name.
func TestReclaimStaleContainers_NeverRemovesUnownedContainer(t *testing.T) {
	tgt, _, dcli := reclaimHarness(t,
		map[string]string{
			composeProjectLabel: "local-developer-sonar",
			composeServiceLabel: "db",
		},
		"local-developer-sonar",
	)

	err := tgt.reclaimStaleContainers(context.Background(), tgt.file, []string{"db"})
	if err != nil {
		t.Fatalf("reclaimStaleContainers: %v", err)
	}
	if len(dcli.calls) != 0 {
		t.Errorf("docker cli calls = %v, want none (unowned container must not be touched)", dcli.calls)
	}
}

// A container owned by the SAME project is not stale; the normal `up -d` path
// recreates it, so Accorda must not reclaim it.
func TestReclaimStaleContainers_DoesNotTouchSameProjectContainer(t *testing.T) {
	tgt, _, dcli := reclaimHarness(t,
		map[string]string{
			composeProjectLabel: "dev-sonar",
			accordaManagedLabel: "true",
			composeServiceLabel: "db",
		},
		"dev-sonar",
	)

	err := tgt.reclaimStaleContainers(context.Background(), tgt.file, []string{"db"})
	if err != nil {
		t.Fatalf("reclaimStaleContainers: %v", err)
	}
	if len(dcli.calls) != 0 {
		t.Errorf("docker cli calls = %v, want none (same-project container is not stale)", dcli.calls)
	}
}

// A service without an explicit container_name has no daemon-wide name to
// reclaim, so no docker operation is issued.
func TestReclaimStaleContainers_NoExplicitName_NoOp(t *testing.T) {
	path := writeSourceCompose(t, `services:
  api:
    image: api:1
`)
	cli := &fakeDockerClient{}
	dcli := &fakeDockerCLI{}
	tgt, err := New(config.Target{Type: config.TargetCompose, File: path}, WithDockerClient(cli), WithDockerCLI(dcli))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tgt.reclaimStaleContainers(context.Background(), tgt.file, []string{"api"}); err != nil {
		t.Fatalf("reclaimStaleContainers: %v", err)
	}
	if len(dcli.calls) != 0 {
		t.Errorf("docker cli calls = %v, want none", dcli.calls)
	}
}

// A failed volume migration must surface as an error and block reclaim so data
// is not silently dropped.
func TestReclaimStaleContainers_VolumeMigrationError_Aborts(t *testing.T) {
	tgt, _, dcli := reclaimHarness(t,
		map[string]string{
			composeProjectLabel: "local-developer-sonar",
			accordaManagedLabel: "true",
			composeServiceLabel: "db",
		},
		"local-developer-sonar",
	)
	dcli.err = errors.New("cp boom")

	err := tgt.reclaimStaleContainers(context.Background(), tgt.file, []string{"db"})
	if err == nil {
		t.Fatal("reclaimStaleContainers with volume copy failure should return error")
	}
	// rm -f must NOT have run after a failed migration.
	if hasRm(dcli.calls, "sonarqube_db") {
		t.Errorf("rm ran despite migration failure: %v", dcli.calls)
	}
}

// Apply must reclaim a stale Accorda-owned container before issuing `up -d`,
// so the name conflict is resolved before Compose creates the new container.
func TestApply_ReclaimsStaleOwnedContainerBeforeUp(t *testing.T) {
	tgt, _, dcli := reclaimHarness(t,
		map[string]string{
			composeProjectLabel: "local-developer-sonar",
			accordaManagedLabel: "true",
			composeServiceLabel: "db",
		},
		"local-developer-sonar",
	)
	runner := &fakeRunner{}
	tgt.runner = runner

	p := plan.New("", "dev", "abc123", time.Now())
	p.AddAction(plan.Action{Kind: plan.ActionCreate, Service: "db"})
	if err := tgt.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// docker rm -f must run before docker compose up.
	if len(dcli.calls) == 0 || !hasRm(dcli.calls, "sonarqube_db") {
		t.Fatalf("docker cli calls = %v, want a stale rm", dcli.calls)
	}
	if len(runner.calls) == 0 {
		t.Fatalf("runner calls = %v, want an up", runner.calls)
	}
}

func hasVolumes(call []string, from, to string) bool {
	joined := ""
	for _, a := range call {
		joined += a + " "
	}
	return containsSub(joined, "-v "+from+":/from") && containsSub(joined, "-v "+to+":/to")
}

func hasRm(calls [][]string, name string) bool {
	for _, c := range calls {
		if len(c) == 3 && c[0] == "rm" && c[1] == "-f" && c[2] == name {
			return true
		}
	}
	return false
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Ensure fakeDockerCLI satisfies the seam.
var _ dockerCli = (*fakeDockerCLI)(nil)
