package compose

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"accorda/internal/core/state"
)

func TestComposeService_AllFields(t *testing.T) {
	service := state.Service{
		Image:   "api:2",
		Command: []string{"serve", "--port", "8080"},
		Env:     map[string]string{"MODE": "production"},
		EnvFiles: []state.ExternalFile{
			{Path: "defaults.env", Required: true},
			{Path: "optional.env", Format: "raw"},
		},
		Ports:    []state.Port{{Host: "8080", Container: "80"}},
		Volumes:  []state.Volume{{Source: "data", Target: "/data", ReadOnly: true}},
		Networks: []string{"backend"},
		Labels:   map[string]string{"tier": "api"},
		LabelFiles: []state.ExternalFile{
			{Path: "labels.env", Required: true},
		},
		DependsOn: []string{"db"},
		Healthcheck: state.Healthcheck{
			Test:        []string{"CMD", "check"},
			Interval:    10 * time.Second,
			Timeout:     2 * time.Second,
			Retries:     3,
			StartPeriod: 5 * time.Second,
			Disable:     true,
		},
	}

	got := composeService(service)
	want := map[string]any{
		"image":       "api:2",
		"command":     []string{"serve", "--port", "8080"},
		"environment": map[string]string{"MODE": "production"},
		"env_file": []any{
			"defaults.env",
			map[string]any{"path": "optional.env", "required": false, "format": "raw"},
		},
		"ports":      []string{"8080:80"},
		"volumes":    []string{"data:/data:ro"},
		"networks":   []string{"backend"},
		"labels":     map[string]string{"tier": "api"},
		"label_file": []string{"labels.env"},
		"depends_on": []string{"db"},
		"healthcheck": map[string]any{
			"test":         []string{"CMD", "check"},
			"interval":     "10s",
			"timeout":      "2s",
			"retries":      3,
			"start_period": "5s",
			"disable":      true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("composeService() = %#v, want %#v", got, want)
	}
}

func TestComposeService_ZeroValueIsEmpty(t *testing.T) {
	if got := composeService(state.Service{}); len(got) != 0 {
		t.Errorf("composeService(zero) = %#v, want empty", got)
	}
	if got := composeHealthcheck(state.Healthcheck{}); got != nil {
		t.Errorf("composeHealthcheck(zero) = %#v, want nil", got)
	}
}

func TestStringPorts(t *testing.T) {
	ports := []state.Port{
		{Container: "80"},
		{Host: "8080", Container: "80", Protocol: "tcp"},
		{HostIP: "127.0.0.1", Host: "5353", Container: "53", Protocol: "udp"},
		{HostIP: "127.0.0.1", Host: "9000"},
	}
	want := []string{"80", "8080:80", "127.0.0.1:5353:53/udp", "127.0.0.1:9000"}
	if got := StringPorts(ports); !reflect.DeepEqual(got, want) {
		t.Errorf("StringPorts() = %v, want %v", got, want)
	}
}

func TestStringVolumes(t *testing.T) {
	volumes := []state.Volume{
		{Target: "/cache"},
		{Source: "data", Target: "/data"},
		{Source: "config", Target: "/config", ReadOnly: true},
		{Source: "scratch", ReadOnly: true},
	}
	want := []string{"/cache", "data:/data", "config:/config:ro", "scratch:ro"}
	if got := StringVolumes(volumes); !reflect.DeepEqual(got, want) {
		t.Errorf("StringVolumes() = %v, want %v", got, want)
	}
}

func TestWriteComposeServices_RoundTripsAllFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "compose.yaml")
	services := map[string]state.Service{
		"api": {
			Image:       "api:2",
			Command:     []string{"serve"},
			Env:         map[string]string{"MODE": "production"},
			EnvFiles:    []state.ExternalFile{{Path: "defaults.env", Required: true}},
			Ports:       []state.Port{{Host: "8080", Container: "80"}},
			Labels:      map[string]string{"tier": "api"},
			LabelFiles:  []state.ExternalFile{{Path: "labels.env", Required: true}},
			Healthcheck: state.Healthcheck{Test: []string{"CMD", "check"}},
			DependsOn:   []string{"db"},
		},
		"db": {Image: "postgres:18"},
	}
	if err := writeComposeServices(path, services); err != nil {
		t.Fatalf("writeComposeServices: %v", err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	want := (&state.DesiredState{Services: services}).Clone().Services
	api := want["api"]
	api.Ports[0].Protocol = "tcp"
	api.Networks = []string{"default"}
	want["api"] = api
	db := want["db"]
	db.Networks = []string{"default"}
	want["db"] = db
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %#v, want %#v", got, want)
	}
}

func TestWriteComposeServices_CreateDirectoryFails(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := writeComposeServices(parent, map[string]state.Service{}); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	err := writeComposeServices(filepath.Join(parent, "compose.yaml"), map[string]state.Service{})
	if err == nil || !strings.Contains(err.Error(), "create dir") {
		t.Errorf("writeComposeServices() error = %v, want create-dir error", err)
	}
}

func TestWriteComposeServices_WriteFails(t *testing.T) {
	err := writeComposeServices(t.TempDir(), map[string]state.Service{})
	if err == nil || !strings.Contains(err.Error(), "compose: write") {
		t.Errorf("writeComposeServices() error = %v, want write error", err)
	}
}
