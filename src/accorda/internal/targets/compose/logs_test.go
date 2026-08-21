package compose

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"accorda/internal/targets"
)

type logCall struct {
	id      string
	options container.LogsOptions
}

type fakeLogDockerClient struct {
	*fakeDockerClient
	data  map[string][]byte
	errs  map[string]error
	calls []logCall
}

func (f *fakeLogDockerClient) ContainerLogs(_ context.Context, id string, options container.LogsOptions) (io.ReadCloser, error) {
	f.calls = append(f.calls, logCall{id: id, options: options})
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(f.data[id])), nil
}

func multiplexedLogs(t *testing.T, stdout, stderr string) []byte {
	t.Helper()
	var stream bytes.Buffer
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stdout).Write([]byte(stdout)); err != nil {
		t.Fatalf("write stdout frame: %v", err)
	}
	if _, err := stdcopy.NewStdWriter(&stream, stdcopy.Stderr).Write([]byte(stderr)); err != nil {
		t.Fatalf("write stderr frame: %v", err)
	}
	return stream.Bytes()
}

func TestLogs_DemultiplexesServiceLogs(t *testing.T) {
	path := writeComposeFile(t)
	project := composeProjectName(path)
	client := &fakeLogDockerClient{
		fakeDockerClient: &fakeDockerClient{
			containers: []container.Summary{summary(project, "api")},
			inspected:  map[string]container.InspectResponse{"id-api": inspect("api:1", "running", "healthy")},
		},
		data: map[string][]byte{"id-api": multiplexedLogs(t, "ready\n", "warning\n")},
	}
	tgt := newTarget(t, path, client)
	var stdout, stderr bytes.Buffer

	err := tgt.Logs(context.Background(), "api", targets.LogOptions{Tail: "25"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if stdout.String() != "ready\n" || stderr.String() != "warning\n" {
		t.Fatalf("stdout=%q stderr=%q, want decoded streams", stdout.String(), stderr.String())
	}
	if len(client.calls) != 1 {
		t.Fatalf("log calls = %d, want 1", len(client.calls))
	}
	wantOptions := container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: "25"}
	if !reflect.DeepEqual(client.calls[0].options, wantOptions) {
		t.Errorf("options = %+v, want %+v", client.calls[0].options, wantOptions)
	}
	labels := client.lastOptions.Filters.Get("label")
	for _, want := range []string{composeProjectLabel + "=" + project, composeServiceLabel + "=api"} {
		if !containsString(labels, want) {
			t.Errorf("label filters = %v, missing %q", labels, want)
		}
	}
}

func TestLogs_TTYStreamIsCopiedRaw(t *testing.T) {
	path := writeComposeFile(t)
	project := composeProjectName(path)
	inspected := inspect("api:1", "running", "healthy")
	inspected.Config.Tty = true
	client := &fakeLogDockerClient{
		fakeDockerClient: &fakeDockerClient{
			containers: []container.Summary{summary(project, "api")},
			inspected:  map[string]container.InspectResponse{"id-api": inspected},
		},
		data: map[string][]byte{"id-api": []byte("raw tty output\n")},
	}
	tgt := newTarget(t, path, client)
	var stdout bytes.Buffer

	if err := tgt.Logs(context.Background(), "api", targets.LogOptions{}, &stdout, nil); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if stdout.String() != "raw tty output\n" {
		t.Errorf("stdout = %q, want raw tty output", stdout.String())
	}
	if client.calls[0].options.Tail != allLogLines {
		t.Errorf("Tail = %q, want %q default", client.calls[0].options.Tail, allLogLines)
	}
}

func TestLogs_FollowPassesOption(t *testing.T) {
	path := writeComposeFile(t)
	project := composeProjectName(path)
	client := &fakeLogDockerClient{
		fakeDockerClient: &fakeDockerClient{
			containers: []container.Summary{summary(project, "api")},
			inspected:  map[string]container.InspectResponse{"id-api": inspect("api:1", "running", "healthy")},
		},
		data: map[string][]byte{"id-api": multiplexedLogs(t, "followed\n", "")},
	}
	tgt := newTarget(t, path, client)

	if err := tgt.Logs(context.Background(), "api", targets.LogOptions{Follow: true}, io.Discard, io.Discard); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(client.calls) != 1 || !client.calls[0].options.Follow {
		t.Errorf("calls = %+v, want Follow=true", client.calls)
	}
}

func TestLogs_ReportsMissingService(t *testing.T) {
	path := writeComposeFile(t)
	client := &fakeLogDockerClient{fakeDockerClient: &fakeDockerClient{}}
	tgt := newTarget(t, path, client)

	err := tgt.Logs(context.Background(), "worker", targets.LogOptions{}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "service \"worker\" has no containers") {
		t.Fatalf("Logs error = %v, want missing-service error", err)
	}
}

func TestLogs_PropagatesDockerErrors(t *testing.T) {
	path := writeComposeFile(t)
	project := composeProjectName(path)
	client := &fakeLogDockerClient{
		fakeDockerClient: &fakeDockerClient{
			containers: []container.Summary{summary(project, "api")},
			inspected:  map[string]container.InspectResponse{"id-api": inspect("api:1", "running", "healthy")},
		},
		errs: map[string]error{"id-api": errors.New("logs unavailable")},
	}
	tgt := newTarget(t, path, client)

	err := tgt.Logs(context.Background(), "api", targets.LogOptions{}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "logs unavailable") {
		t.Fatalf("Logs error = %v, want Docker error", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
