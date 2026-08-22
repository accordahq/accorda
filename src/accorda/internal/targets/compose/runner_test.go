package compose

import (
	"context"
	"strings"
	"testing"
)

func TestCLIRunnerReportsCommandFailure(t *testing.T) {
	t.Setenv("PATH", "")
	err := (cliRunner{file: "compose.yaml", project: "example"}).Run(context.Background(), "up", "-d", "api")
	if err == nil {
		t.Fatal("Run() error = nil, want missing-docker error")
	}
	for _, want := range []string{"docker compose up -d api", "executable file not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Run() error = %q, want %q", err, want)
		}
	}
}
