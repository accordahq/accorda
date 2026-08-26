package state

import (
	"reflect"
	"testing"
)

func TestStringPorts(t *testing.T) {
	ports := []Port{
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
	volumes := []Volume{
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
