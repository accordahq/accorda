package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"accorda/internal/core/state"
)

const fullCompose = `services:
  api:
    image: ghcr.io/acme/api:2.4.1
    command: ["./api", "--port", "8080"]
    environment:
      LOG_LEVEL: warning
      DEBUG: "true"
    ports:
      - "8080:8080"
      - target: 9090
        published: 9090
        host_ip: 127.0.0.1
        protocol: tcp
    volumes:
      - /etc/api:/etc/api:ro
      - type: volume
        source: data
        target: /data
    networks:
      - frontend
    labels:
      app: api
      tier: web
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 5s
      timeout: 2s
      retries: 10
    depends_on:
      - postgres
  postgres:
    image: postgres:17
    environment:
      - POSTGRES_PASSWORD=secret
      - POSTGRES_USER
    volumes:
      - /var/lib/postgresql/data
    networks:
      backend: {}
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 30
`

func TestParse_FullExample(t *testing.T) {
	services, err := Parse([]byte(fullCompose))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(services), services)
	}
	t.Run("api", func(t *testing.T) { assertAPIService(t, services["api"]) })
	t.Run("postgres", func(t *testing.T) { assertPostgresService(t, services["postgres"]) })
}

// assertAPIService checks the normalized fields of the `api` service.
func assertAPIService(t *testing.T, api state.Service) {
	t.Helper()
	if api.Image != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("api.Image = %q, want ghcr.io/acme/api:2.4.1", api.Image)
	}
	wantCmd := []string{"./api", "--port", "8080"}
	if !reflect.DeepEqual(api.Command, wantCmd) {
		t.Errorf("api.Command = %v, want %v", api.Command, wantCmd)
	}
	assertEnv(t, "api", api.Env, map[string]string{"LOG_LEVEL": "warning", "DEBUG": "true"})
	assertAPIPorts(t, api.Ports)
	assertAPIVolumes(t, api.Volumes)
	if !reflect.DeepEqual(api.Networks, []string{"frontend"}) {
		t.Errorf("api.Networks = %v, want [frontend]", api.Networks)
	}
	assertLabels(t, "api", api.Labels, map[string]string{"app": "api", "tier": "web"})
	assertAPIHealthcheck(t, api.Healthcheck)
	if !reflect.DeepEqual(api.DependsOn, []string{"postgres"}) {
		t.Errorf("api.DependsOn = %v, want [postgres]", api.DependsOn)
	}
}

// assertPostgresService checks the normalized fields of the `postgres` service.
func assertPostgresService(t *testing.T, pg state.Service) {
	t.Helper()
	if pg.Env["POSTGRES_PASSWORD"] != "secret" {
		t.Errorf("postgres.Env[POSTGRES_PASSWORD] = %q, want secret", pg.Env["POSTGRES_PASSWORD"])
	}
	if _, ok := pg.Env["POSTGRES_USER"]; !ok {
		t.Errorf("postgres.Env missing POSTGRES_USER, got %+v", pg.Env)
	}
	if len(pg.Volumes) != 1 || pg.Volumes[0].Target != "/var/lib/postgresql/data" {
		t.Errorf("postgres.Volumes = %+v, want anonymous target /var/lib/postgresql/data", pg.Volumes)
	}
	if !reflect.DeepEqual(pg.Networks, []string{"backend"}) {
		t.Errorf("postgres.Networks = %v, want [backend]", pg.Networks)
	}
	if pg.Healthcheck.Test[0] != "CMD-SHELL" || pg.Healthcheck.Test[1] != "pg_isready" {
		t.Errorf("postgres.Healthcheck.Test = %v, want [CMD-SHELL pg_isready]", pg.Healthcheck.Test)
	}
	if pg.Healthcheck.Interval != 30*time.Second {
		t.Errorf("postgres.Healthcheck.Interval = %v, want 30s (bare-integer seconds)", pg.Healthcheck.Interval)
	}
}

// assertEnv checks that env matches want for the named service.
func assertEnv(t *testing.T, name string, env, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s.Env[%s] = %q, want %q", name, k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("%s.Env = %v, want %v", name, env, want)
	}
}

// assertAPIPorts checks the two normalized port entries for `api`.
func assertAPIPorts(t *testing.T, ports []state.Port) {
	t.Helper()
	if len(ports) != 2 {
		t.Fatalf("api.Ports = %v, want 2 entries", ports)
	}
	wantP0 := state.Port{Host: "8080", Container: "8080", Protocol: "tcp"}
	if ports[0] != wantP0 {
		t.Errorf("api.Ports[0] = %+v, want %+v", ports[0], wantP0)
	}
	wantP1 := state.Port{HostIP: "127.0.0.1", Host: "9090", Container: "9090", Protocol: "tcp"}
	if ports[1] != wantP1 {
		t.Errorf("api.Ports[1] = %+v, want %+v", ports[1], wantP1)
	}
}

// assertAPIVolumes checks the two normalized volume entries for `api`.
func assertAPIVolumes(t *testing.T, vols []state.Volume) {
	t.Helper()
	if len(vols) != 2 {
		t.Fatalf("api.Volumes = %v, want 2 entries", vols)
	}
	wantV0 := state.Volume{Type: "bind", Source: "/etc/api", Target: "/etc/api", ReadOnly: true}
	if vols[0] != wantV0 {
		t.Errorf("api.Volumes[0] = %+v, want %+v", vols[0], wantV0)
	}
	wantV1 := state.Volume{Type: "volume", Source: "data", Target: "/data"}
	if vols[1] != wantV1 {
		t.Errorf("api.Volumes[1] = %+v, want %+v", vols[1], wantV1)
	}
}

// assertLabels checks that labels matches want for the named service.
func assertLabels(t *testing.T, name string, labels, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("%s.Labels[%s] = %q, want %q", name, k, labels[k], v)
		}
	}
	if len(labels) != len(want) {
		t.Errorf("%s.Labels = %v, want %v", name, labels, want)
	}
}

// assertAPIHealthcheck checks the normalized healthcheck for `api`.
func assertAPIHealthcheck(t *testing.T, hc state.Healthcheck) {
	t.Helper()
	wantHC := state.Healthcheck{
		Test:     []string{"CMD", "curl", "-f", "http://localhost:8080/health"},
		Interval: 5 * time.Second,
		Timeout:  2 * time.Second,
		Retries:  10,
	}
	if !reflect.DeepEqual(hc.Test, wantHC.Test) ||
		hc.Interval != wantHC.Interval ||
		hc.Timeout != wantHC.Timeout ||
		hc.Retries != wantHC.Retries {
		t.Errorf("api.Healthcheck = %+v, want %+v", hc, wantHC)
	}
}

func TestParse_CommandShellForm(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    command: ./api --port 8080
`)
	services, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"./api --port 8080"}
	if !reflect.DeepEqual(services["api"].Command, want) {
		t.Errorf("Command = %v, want %v", services["api"].Command, want)
	}
}

func TestParse_HealthcheckScalarTest(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    healthcheck:
      test: curl -f http://localhost:8080/health
`)
	services, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"CMD-SHELL", "curl -f http://localhost:8080/health"}
	if got := services["api"].Healthcheck.Test; !reflect.DeepEqual(got, want) {
		t.Errorf("healthcheck test = %v, want %v", got, want)
	}
}

func TestParse_EmptyDocument(t *testing.T) {
	services, err := Parse([]byte(``))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(services) != 0 {
		t.Errorf("got %d services, want 0", len(services))
	}
}

func TestParse_NoServicesKey(t *testing.T) {
	services, err := Parse([]byte(`version: "3"
networks:
  frontend: {}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(services) != 0 {
		t.Errorf("got %d services, want 0", len(services))
	}
}

func TestParse_MissingImage_IsError(t *testing.T) {
	data := []byte(`services:
  api:
    command: ["./api"]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for service without image, got nil")
	}
}

func TestParse_BuildWithoutImage_IsError(t *testing.T) {
	data := []byte(`services:
  api:
    build: .
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for build-only service, got nil")
	}
}

func TestParse_PortWithoutContainer_IsError(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    ports:
      - published: "8080"
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for port without container, got nil")
	}
}

func TestParse_VolumeWithoutTarget_IsError(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    volumes:
      - type: volume
        source: data
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for volume without target, got nil")
	}
}

func TestParse_UnknownServiceField_IsError(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    bogus_field: oops
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for unknown service field, got nil")
	}
}

func TestParse_EmptyServiceName_IsError(t *testing.T) {
	data := []byte(`services:
  "":
    image: api:1
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for empty service name, got nil")
	}
}

func TestParse_NotAMapping_IsError(t *testing.T) {
	if _, err := Parse([]byte("- one\n- two\n")); err == nil {
		t.Fatal("expected error for non-mapping root, got nil")
	}
}

func TestParse_ServicesNotAMapping_IsError(t *testing.T) {
	data := []byte(`services:
  - api
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for non-mapping services, got nil")
	}
}

func TestParse_PortsListAndStringForms(t *testing.T) {
	cases := []struct {
		in   string
		want state.Port
	}{
		{"8080", state.Port{Container: "8080", Protocol: "tcp"}},
		{"8080:8080", state.Port{Host: "8080", Container: "8080", Protocol: "tcp"}},
		{"127.0.0.1:8080:8080", state.Port{HostIP: "127.0.0.1", Host: "8080", Container: "8080", Protocol: "tcp"}},
		{"8080/udp", state.Port{Container: "8080", Protocol: "udp"}},
		{"8080-8085:8080-8085/tcp", state.Port{Host: "8080-8085", Container: "8080-8085", Protocol: "tcp"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			data := []byte("services:\n  api:\n    image: api:1\n    ports:\n      - " + c.in + "\n")
			services, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := services["api"].Ports[0]; got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParse_EnvironmentMappingNullValue(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    environment:
      UNSET_VAR:
`)
	services, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v, ok := services["api"].Env["UNSET_VAR"]; !ok || v != "" {
		t.Errorf("Env[UNSET_VAR] = %q (ok=%v), want empty string present", v, ok)
	}
}

func TestParse_LabelsListForm(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    labels:
      - app=api
      - tier=web
`)
	services, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if services["api"].Labels["app"] != "api" || services["api"].Labels["tier"] != "web" {
		t.Errorf("labels = %v", services["api"].Labels)
	}
}

func TestParse_DependsOnMappingForm(t *testing.T) {
	data := []byte(`services:
  api:
    image: api:1
    depends_on:
      postgres:
        condition: service_healthy
      redis: {}
`)
	services, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"postgres", "redis"}
	if got := services["api"].DependsOn; !reflect.DeepEqual(got, want) {
		t.Errorf("depends_on = %v, want %v", got, want)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(fullCompose), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	services, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if services["api"].Image != "ghcr.io/acme/api:2.4.1" {
		t.Errorf("api.Image = %q", services["api"].Image)
	}
}

func TestLoadFile_Missing(t *testing.T) {
	_, err := LoadFile("/nonexistent/compose.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
