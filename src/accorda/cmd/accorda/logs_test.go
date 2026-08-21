package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_LogsRequiresService(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"logs"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "accepts 1 arg(s)") {
		t.Fatalf("run(logs) error = %v, want required-service error", err)
	}
}

func TestRun_LogsIsImplementedAndRequiresConfig(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"logs", "api", "--dir", t.TempDir()}, &out, &out)
	if err == nil {
		t.Fatal("run(logs): expected config error, got nil")
	}
	if strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("run(logs): unexpected stub error: %v", err)
	}
}

func TestLogsFlags(t *testing.T) {
	cmd := newLogsCmd()
	follow := cmd.Flags().Lookup("follow")
	tail := cmd.Flags().Lookup("tail")
	if follow == nil || follow.Shorthand != "f" {
		t.Fatalf("follow flag = %+v, want -f shorthand", follow)
	}
	if tail == nil || tail.DefValue != "all" {
		t.Fatalf("tail flag = %+v, want default all", tail)
	}
}
