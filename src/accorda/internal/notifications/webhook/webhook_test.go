package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
	"accorda/internal/secrets"
)

// mockServer is a minimal HTTP server that records received requests for
// assertions. It is the in-process equivalent of the mock server the issue
// asks for (docs/ACCORDA.md §21: "Tests with mock server").
type mockServer struct {
	mu       sync.Mutex
	requests []recordedRequest
	status   int
	bodyFn   func([]byte) // optional hook for per-request assertions
	hang     func()       // optional hook that blocks a request if set
}

type recordedRequest struct {
	body    []byte
	headers http.Header
	method  string
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	return &mockServer{status: http.StatusOK}
}

func (m *mockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.requests = append(m.requests, recordedRequest{body: body, headers: r.Header.Clone(), method: r.Method})
	if m.bodyFn != nil {
		m.bodyFn(body)
	}
	status := m.status
	hang := m.hang
	m.mu.Unlock()
	if hang != nil {
		hang() // block the request so the delivery goroutine stays in flight
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("ok"))
}

func (m *mockServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *mockServer) lastBody() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1].body
}

func (m *mockServer) lastHeaders() http.Header {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[len(m.requests)-1].headers
}

func baseCfg(url string) config.WebhookConfig {
	return config.WebhookConfig{URL: url, MaxRetries: 2, Timeout: time.Second}
}

func TestNew_RejectsEmptyURL(t *testing.T) {
	if _, err := New(config.WebhookConfig{}); err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestNew_RejectsNegativeRetries(t *testing.T) {
	_, err := New(config.WebhookConfig{URL: "http://x", MaxRetries: -1})
	if err == nil {
		t.Fatal("expected error for negative retries, got nil")
	}
}

func TestDeliver_PostsJSONEnvelope(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr), WithClock(func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := con.deliver(context.Background(), events.Event{Type: events.EventDeploymentSucceeded}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	var env map[string]any
	if err := json.Unmarshal(srv.lastBody(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env["type"] != events.EventDeploymentSucceeded {
		t.Errorf("type = %v, want %q", env["type"], events.EventDeploymentSucceeded)
	}
	if env["timestamp"] != "2023-11-14T22:13:20Z" {
		t.Errorf("timestamp = %v, want RFC3339 of fixed clock", env["timestamp"])
	}
	if env["payload"] != nil {
		t.Errorf("payload = %v, want nil for empty payload", env["payload"])
	}
	if ct := srv.lastHeaders().Get("Content-Type"); ct != contentType {
		t.Errorf("Content-Type = %q, want %q", ct, contentType)
	}
}

func TestDeliver_RetriesOn5xxThenSucceeds(t *testing.T) {
	srv := newMockServer(t)
	var calls atomic.Int32
	srv.status = http.StatusInternalServerError
	// Flip to 200 on the 3rd attempt (1 initial + 2 retries).
	srv.bodyFn = func([]byte) {
		if calls.Add(1) == 3 {
			srv.status = http.StatusOK
		}
	}
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr), WithSleeper(func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := con.deliver(context.Background(), events.Event{Type: events.EventDeploymentFailed}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := srv.count(); got != 3 {
		t.Fatalf("requests = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestDeliver_RetriesExhausted_ReportsError(t *testing.T) {
	srv := newMockServer(t)
	srv.status = http.StatusBadGateway
	addr := startMock(t, srv)
	var reported atomic.Int32
	con, err := New(baseCfg(addr),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithErrorSink(func(err error) { reported.Add(1) }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Handle dispatches asynchronously; wait for the delivery to complete.
	con.Handle(context.Background(), events.Event{Type: events.EventDeploymentFailed})
	// 1 initial + 2 retries = 3 attempts.
	if !waitFor(t, func() bool { return srv.count() == 3 }) {
		t.Fatalf("requests = %d, want 3", srv.count())
	}
	if !waitFor(t, func() bool { return reported.Load() == 1 }) {
		t.Errorf("error sink calls = %d, want 1", reported.Load())
	}
}

func TestDeliver_NetworkError_Retries(t *testing.T) {
	// Point at a closed port to trigger a transport error.
	con, err := New(config.WebhookConfig{URL: "http://127.0.0.1:1", MaxRetries: 2, Timeout: 100 * time.Millisecond},
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var reported atomic.Int32
	con.onError = func(err error) { reported.Add(1) }
	con.Handle(context.Background(), events.Event{Type: events.EventDeploymentDetected})
	if !waitFor(t, func() bool { return reported.Load() == 1 }) {
		t.Errorf("error sink calls = %d, want 1", reported.Load())
	}
}

func TestDeliver_4xxNotRetried(t *testing.T) {
	srv := newMockServer(t)
	srv.status = http.StatusBadRequest
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr), WithSleeper(func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := con.deliver(context.Background(), events.Event{Type: events.EventDeploymentDetected}); err == nil {
		t.Fatal("expected error for 4xx, got nil")
	}
	if got := srv.count(); got != 1 {
		t.Fatalf("requests = %d, want 1 (4xx not retried)", got)
	}
}

func TestDeliver_HMACSignatureHeader(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	cfg := baseCfg(addr)
	cfg.Secret = "topsecret"
	con, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := con.deliver(context.Background(), events.Event{Type: events.EventDeploymentSucceeded}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	sig := srv.lastHeaders().Get(signatureHeader)
	if sig == "" {
		t.Fatal("signature header missing")
	}
	want := "sha256=" + hmacHex("topsecret", srv.lastBody())
	if sig != want {
		t.Errorf("signature = %q, want %q", sig, want)
	}
}

func TestDeliver_NoSignatureWhenSecretEmpty(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := con.deliver(context.Background(), events.Event{Type: events.EventDeploymentSucceeded}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if sig := srv.lastHeaders().Get(signatureHeader); sig != "" {
		t.Errorf("signature = %q, want empty", sig)
	}
}

func TestDeliver_CancelledContext_Stops(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr), WithSleeper(func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.status = http.StatusInternalServerError
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before delivery so the first retry sleep aborts
	if err := con.deliver(ctx, events.Event{Type: events.EventDeploymentFailed}); err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if got := srv.count(); got > 1 {
		t.Errorf("requests = %d, want <=1 after cancellation", got)
	}
}

func TestSubscribe_NilBus_NoOp(t *testing.T) {
	con, err := New(config.WebhookConfig{URL: "http://x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	unsub := con.Subscribe(nil)
	unsub() // must not panic
}

func TestHandle_DoesNotBlockPublishPath(t *testing.T) {
	// Handle must return immediately even when the endpoint is unreachable and
	// retries would otherwise take seconds: delivery is dispatched to a
	// goroutine so it never blocks the synchronous event bus
	// (docs/DECISIONS.md #46).
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(config.WebhookConfig{URL: addr, MaxRetries: 5, Timeout: time.Hour},
		WithSleeper(func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.status = http.StatusInternalServerError

	done := make(chan struct{})
	go func() {
		con.Handle(context.Background(), events.Event{Type: events.EventDeploymentStarted})
		close(done)
	}()
	select {
	case <-done:
		// Handle returned immediately; delivery continues in the background.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Handle blocked the publish path")
	}
}

func TestPost_RejectsRedirect(t *testing.T) {
	// The webhook client must not follow a 3xx from the receiver, which could
	// otherwise pivot the payload to an internal address (SSRF).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	redirect := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})}
	go func() { _ = redirect.Serve(ln) }()
	t.Cleanup(func() { _ = redirect.Close() })

	addr := "http://" + ln.Addr().String()
	con, err := New(config.WebhookConfig{URL: addr, MaxRetries: 0, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = con.deliver(context.Background(), events.Event{Type: events.EventDeploymentStarted})
	if err == nil {
		t.Fatal("expected redirect to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "redirects are not followed") {
		t.Errorf("error = %v, want redirect-rejection", err)
	}
}

func TestSubscribe_DeliversViaBus(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bus := events.NewBus()
	unsub := con.Subscribe(bus)
	defer unsub()
	// Handle dispatches asynchronously (docs/DECISIONS.md #46), so wait for
	// the goroutine to deliver.
	bus.Publish(context.Background(), events.Event{Type: events.EventDeploymentStarted})
	if !waitFor(t, func() bool { return srv.count() == 1 }) {
		t.Fatal("webhook not delivered within timeout")
	}
	unsub()
	bus.Publish(context.Background(), events.Event{Type: events.EventDeploymentStarted})
	// Allow any in-flight delivery to land, then require exactly one.
	if !waitFor(t, func() bool { return srv.count() >= 1 }) {
		t.Fatal("no deliveries observed")
	}
	time.Sleep(20 * time.Millisecond)
	if got := srv.count(); got != 1 {
		t.Errorf("requests after unsubscribe = %d, want 1", got)
	}
}

// waitFor polls cond until it reports true or a short timeout elapses.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestHandle_ConcurrencyLimit_DropsEvents(t *testing.T) {
	// Handle must bound in-flight deliveries and drop (with an error-sink
	// report) rather than block or grow goroutines unboundedly.
	srv := newMockServer(t)
	addr := startMock(t, srv)
	block := make(chan struct{})
	// Each request hangs until block is closed, so every accepted delivery
	// holds a concurrency slot.
	srv.hang = func() { <-block }
	var dropped atomic.Int32
	con, err := New(baseCfg(addr),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithErrorSink(func(err error) {
			if strings.Contains(err.Error(), "dropped") {
				dropped.Add(1)
			}
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Fill the budget plus one overflow event.
	for i := 0; i < maxConcurrentDeliveries+1; i++ {
		con.Handle(context.Background(), events.Event{Type: events.EventDeploymentStarted})
	}
	if !waitFor(t, func() bool { return srv.count() == maxConcurrentDeliveries }) {
		t.Fatalf("in-flight deliveries = %d, want %d", srv.count(), maxConcurrentDeliveries)
	}
	if !waitFor(t, func() bool { return dropped.Load() == 1 }) {
		t.Errorf("dropped events = %d, want 1", dropped.Load())
	}
	close(block)
}

func TestRedactErrorText_StripsURLCredentials(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   `git source: clone "https://user:ghp_token@github.com/acme/infra.git": authentication required`,
			want: `git source: clone "https://github.com/acme/infra.git": authentication required`,
		},
		{
			in:   `dial tcp: lookup https://user:pass@host.example: no such host`,
			want: `dial tcp: lookup https://host.example: no such host`,
		},
		{
			in:   "no url here",
			want: "no url here",
		},
		{
			in:   `ssh://git@github.com/acme/infra.git: permission denied`,
			want: `ssh://github.com/acme/infra.git: permission denied`,
		},
		{
			in:   "at sign but no scheme @example.com",
			want: "at sign but no scheme @example.com",
		},
	}
	for _, c := range cases {
		if got := redactErrorText(c.in); got != c.want {
			t.Errorf("redactErrorText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactTransition_RedactsErrorURL(t *testing.T) {
	tr := reconcile.StateTransition{
		From:         reconcile.PhaseFetching,
		To:           reconcile.PhaseFailed,
		Commit:       "abc123",
		DeploymentID: "dep_1",
		Err:          errors.New(`git source: clone "https://user:token@github.com/acme/infra.git": boom`),
	}
	got := redactPayload(tr)
	m, _ := got.(map[string]any)
	if m["error"] != `git source: clone "https://github.com/acme/infra.git": boom` {
		t.Errorf("error = %q, want redacted URL", m["error"])
	}
	if strings.Contains(m["error"].(string), "token") {
		t.Error("error leaks the credential")
	}
}

func TestRedactPayload_HealthHasNoSecrets(t *testing.T) {
	h := health.New(time.Unix(0, 0))
	h.SetService("api", health.StatusHealthy, "")
	h.Summarize()
	got := redactPayload(&h)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("redactPayload(Health) = %T, want map", got)
	}
	if m["overall"] != string(health.StatusHealthy) {
		t.Errorf("overall = %v, want %q", m["overall"], health.StatusHealthy)
	}
}

func TestRedactPayload_DesiredStateRedactsEnv(t *testing.T) {
	desired := &state.DesiredState{
		Repository: "acme/infra",
		Branch:     "main",
		Commit:     "abc123",
		Services: map[string]state.Service{
			"api": {
				Image: "ghcr.io/acme/api:2.4.1",
				Env:   map[string]string{"DB_PASSWORD": "super-secret", "API_KEY": "12345"},
			},
		},
	}
	got := redactPayload(desired)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("redactPayload(DesiredState) = %T, want map", got)
	}
	services, _ := m["services"].(map[string]any)
	api, _ := services["api"].(map[string]any)
	env, _ := api["env"].(map[string]string)
	if env["DB_PASSWORD"] != secrets.RedactedValue {
		t.Errorf("DB_PASSWORD = %q, want %q", env["DB_PASSWORD"], secrets.RedactedValue)
	}
	if env["API_KEY"] != secrets.RedactedValue {
		t.Errorf("API_KEY = %q, want %q", env["API_KEY"], secrets.RedactedValue)
	}
}

func TestRedactPayload_DeployedStateRedactsEnv(t *testing.T) {
	deployed := &state.DeployedState{
		DeploymentID: "dep_1",
		Commit:       "abc123",
		Services: map[string]state.Service{
			"api": {Image: "img:1", Env: map[string]string{"TOKEN": "s3cr3t"}},
		},
	}
	got := redactPayload(deployed)
	m, _ := got.(map[string]any)
	services, _ := m["services"].(map[string]any)
	api, _ := services["api"].(map[string]any)
	env, _ := api["env"].(map[string]string)
	if env["TOKEN"] != secrets.RedactedValue {
		t.Errorf("TOKEN = %q, want %q", env["TOKEN"], secrets.RedactedValue)
	}
}

func TestRedactPayload_StateTransitionRendersError(t *testing.T) {
	tr := reconcile.StateTransition{
		From:         reconcile.PhaseDeploying,
		To:           reconcile.PhaseFailed,
		Commit:       "abc123",
		DeploymentID: "dep_1",
		Err:          errors.New("boom"),
	}
	got := redactPayload(tr)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("redactPayload(StateTransition) = %T, want map", got)
	}
	if m["error"] != "boom" {
		t.Errorf("error = %v, want %q", m["error"], "boom")
	}
	if m["to"] != string(reconcile.PhaseFailed) {
		t.Errorf("to = %v, want %q", m["to"], reconcile.PhaseFailed)
	}
}

func TestRedactPayload_UnknownTypeReturnedUnchanged(t *testing.T) {
	v := struct{ X int }{X: 42}
	if got := redactPayload(v); got != v {
		t.Errorf("redactPayload(unknown) = %v, want %v", got, v)
	}
}

func TestMarshal_IncludesRedactedEnv(t *testing.T) {
	srv := newMockServer(t)
	addr := startMock(t, srv)
	con, err := New(baseCfg(addr), WithClock(func() time.Time { return time.Unix(0, 0).UTC() }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	desired := &state.DesiredState{
		Services: map[string]state.Service{
			"api": {Env: map[string]string{"SECRET": "leak-me"}},
		},
	}
	con.Handle(context.Background(), events.Event{Type: "custom.event", Payload: desired})
	if !waitFor(t, func() bool { return srv.count() == 1 }) {
		t.Fatal("webhook not delivered after timeout")
	}
	var env map[string]any
	if err := json.Unmarshal(srv.lastBody(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	payload, _ := env["payload"].(map[string]any)
	services, _ := payload["services"].(map[string]any)
	api, _ := services["api"].(map[string]any)
	// JSON decodes map[string]string values as map[string]any.
	envMap, _ := api["env"].(map[string]any)
	if envMap["SECRET"] != secrets.RedactedValue {
		t.Errorf("SECRET in wire payload = %v, want %q", envMap["SECRET"], secrets.RedactedValue)
	}
	if bytes.Contains(srv.lastBody(), []byte("leak-me")) {
		t.Error("payload contains plaintext secret \"leak-me\"")
	}
}

func TestRetryable_ClassifiesErrors(t *testing.T) {
	if !retryable(retryableError{status: 500}) {
		t.Error("500 should be retryable")
	}
	if !retryable(retryableError{status: 429}) {
		t.Error("429 should be retryable")
	}
	if retryable(retryableError{status: 400}) {
		t.Error("400 should not be retryable")
	}
	if !retryable(retryableError{err: errors.New("transport")}) {
		t.Error("transport error should be retryable")
	}
}

// startMock starts srv on an ephemeral port and returns its base URL.
func startMock(t *testing.T, srv *mockServer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: srv}
	go func() { _ = server.Serve(ln) }()
	addr := "http://" + ln.Addr().String()
	t.Cleanup(func() { _ = server.Close() })
	return addr
}

// hmacHex computes the HMAC-SHA256 of body keyed by secret as lowercase hex,
// mirroring the production signature helper for assertion.
func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
