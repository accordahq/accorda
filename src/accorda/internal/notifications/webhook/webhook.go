package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"accorda/internal/config"
	"accorda/internal/core/events"
	"accorda/internal/core/health"
	"accorda/internal/core/reconcile"
	"accorda/internal/core/state"
	"accorda/internal/secrets"
)

// contentType is the media type used for webhook deliveries.
const contentType = "application/json"

// signatureHeader is the HTTP header carrying the HMAC-SHA256 payload
// signature when a shared secret is configured (docs/ACCORDA.md §21).
const signatureHeader = "X-Accorda-Signature"

// minRetryBackoff is the base delay for the first retry; subsequent retries
// double the delay, capped at maxRetryBackoff (docs/ACCORDA.md §21).
const (
	minRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff = 10 * time.Second
)

// errorSink receives non-fatal delivery errors. A failed webhook must not
// block reconciliation, so errors are reported here rather than returned to
// the event bus. A nil sink drops error reports silently.
type errorSink func(err error)

// maxConcurrentDeliveries bounds the number of in-flight delivery goroutines.
// A single reconcile cycle emits many events, and in --watch mode cycles run
// continuously; against a slow endpoint the per-event delivery can live for
// the whole retry budget, so unbounded goroutines would leak and exhaust the
// process. Bounding concurrency caps in-flight deliveries; when the limit is
// reached, Handle drops the event and reports it to the error sink rather
// than blocking the bus publish path (docs/DECISIONS.md #41).
const maxConcurrentDeliveries = 16

// Consumer is a generic outbound webhook notification target
// (docs/ACCORDA.md §21). It subscribes to the core event bus and POSTs each
// event as a JSON payload to the configured URL, retrying transient failures
// up to MaxRetries times with exponential backoff. Secret values in the
// payload are redacted before serialization.
//
// Delivery is best-effort and asynchronous: Handle dispatches each event to a
// goroutine so retries and backoff never run on the bus publish path and
// cannot block the reconcile loop. Concurrency is bounded to
// maxConcurrentDeliveries; when the limit is reached Handle drops the event
// and reports a delivery-limit error rather than blocking or growing
// unboundedly. The event bus may invoke Handle from multiple goroutines; the
// Consumer is safe for concurrent use and each delivery is independent, so
// ordering is not guaranteed.
type Consumer struct {
	cfg     config.WebhookConfig
	client  *http.Client
	now     func() time.Time
	sleeper func(context.Context, time.Duration) error
	onError errorSink
	sem     chan struct{}
}

// Option configures a Consumer.
type Option func(*Consumer)

// WithClock sets the clock used to stamp payload timestamps. It is intended
// for tests.
func WithClock(now func() time.Time) Option {
	return func(con *Consumer) { con.now = now }
}

// WithSleeper sets the sleep function used between retries. It is intended
// for tests; the default sleeps for the requested duration, respecting ctx.
func WithSleeper(s func(context.Context, time.Duration) error) Option {
	return func(con *Consumer) { con.sleeper = s }
}

// WithErrorSink sets the function that receives non-fatal delivery errors.
// A nil sink (the default) drops error reports silently.
func WithErrorSink(s func(err error)) Option {
	return func(con *Consumer) { con.onError = s }
}

// New constructs a Consumer from the validated webhook configuration. The
// returned Consumer is not yet subscribed; call Subscribe on the event bus
// with Consumer.Handle to begin delivery, and keep the returned unsubscribe
// function to stop.
func New(cfg config.WebhookConfig, opts ...Option) (*Consumer, error) {
	if cfg.URL == "" {
		return nil, errors.New("webhook: url is required")
	}
	if cfg.MaxRetries < 0 {
		return nil, errors.New("webhook: max_retries must be non-negative")
	}
	if cfg.Timeout < 0 {
		return nil, errors.New("webhook: timeout must be non-negative")
	}
	c := &Consumer{
		cfg:     cfg,
		client:  withoutRedirects(cfg.Timeout),
		now:     time.Now,
		sleeper: sleepCtx,
		sem:     make(chan struct{}, maxConcurrentDeliveries),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// withoutRedirects returns an HTTP client that refuses to follow redirects.
// The configured webhook URL is operator-controlled, so the initial request
// is trusted, but a compromised or malicious receiver could otherwise return
// a 3xx that pivots the (redacted) payload to an internal address. Rejecting
// redirects closes that path (docs/ACCORDA.md §21).
func withoutRedirects(timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 16
	t.MaxIdleConnsPerHost = 8
	if timeout <= 0 {
		timeout = config.DefaultWebhookTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: t,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("webhook: redirects are not followed")
		},
	}
}

// Subscribe registers the Consumer on bus and returns an unsubscribe
// function. Calling it removes the Consumer from the bus; it is safe to call
// more than once. A nil bus is a no-op that returns a no-op unsubscribe so
// callers can defer it uniformly regardless of whether a bus is configured.
func (c *Consumer) Subscribe(bus events.Bus) func() {
	if bus == nil {
		return func() {
			// Nothing was subscribed, so there is nothing to unsubscribe; keep
			// c in scope so the no-op is not a bare empty function body.
			_ = c.cfg.URL
		}
	}
	return bus.Subscribe(c.Handle)
}

// Handle is the event bus handler that delivers one event as a webhook. It
// never blocks the bus publish path: each event is dispatched to a goroutine
// so retries and backoff run off the reconcile loop (the event bus is
// synchronous; docs/DECISIONS.md #41). It never panics across the bus
// boundary: a delivery error is reported to the error sink and does not
// propagate.
func (c *Consumer) Handle(ctx context.Context, e events.Event) {
	select {
	case c.sem <- struct{}{}:
		go c.deliverAsync(ctx, e)
	default:
		// Concurrency budget is exhausted. Drop the event rather than block
		// the reconcile loop or grow goroutines unboundedly; report the drop
		// so an operator can see delivery is being throttled.
		if c.onError != nil {
			c.onError(fmt.Errorf("webhook %s: dropped: delivery concurrency limit (%d) reached", e.Type, maxConcurrentDeliveries))
		}
	}
}

// deliverAsync delivers e and reports any failure to the error sink. It is
// the goroutine entry point for Handle and releases one concurrency slot on
// return.
func (c *Consumer) deliverAsync(ctx context.Context, e events.Event) {
	defer func() { <-c.sem }()
	if err := c.deliver(ctx, e); err != nil {
		if c.onError != nil {
			c.onError(fmt.Errorf("webhook %s: %w", e.Type, err))
		}
	}
}

// deliver serializes e into a redacted JSON payload and POSTs it to the
// configured URL, retrying transient failures up to MaxRetries times.
func (c *Consumer) deliver(ctx context.Context, e events.Event) error {
	body, err := c.marshal(e)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	backoff := minRetryBackoff
	maxAttempts := c.cfg.MaxRetries + 1
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = c.post(ctx, body)
		if lastErr == nil {
			return nil
		}
		if !retryable(lastErr) || attempt == maxAttempts {
			return lastErr
		}
		if err := c.sleeper(ctx, backoff); err != nil {
			return err
		}
		backoff = doubleBackoff(backoff)
	}
	return lastErr
}

// post sends one HTTP request with the payload body, returning nil on a 2xx
// response. A non-2xx response is a retryable error carrying the status code.
// The per-request timeout is enforced by the Consumer's HTTP client, and
// redirects are rejected (see withoutRedirects) so a 3xx cannot pivot the
// payload to another host.
func (c *Consumer) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if c.cfg.Secret != "" {
		req.Header.Set(signatureHeader, signature(c.cfg.Secret, body))
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return retryableError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return retryableError{status: resp.StatusCode}
}

// marshal builds the JSON envelope for an event, redacting secret values in
// the payload before serialization (docs/ACCORDA.md §21).
func (c *Consumer) marshal(e events.Event) ([]byte, error) {
	envelope := map[string]any{
		"type":      e.Type,
		"timestamp": c.now().UTC().Format(time.RFC3339Nano),
		"payload":   redactPayload(e.Payload),
	}
	return json.Marshal(envelope)
}

// redactPayload converts a core event payload into a JSON-safe, redacted
// representation. It handles the concrete payload types the reconciler
// emits: *health.Health, state.Comparison, and reconcile.StateTransition.
// State payloads that carry per-service Env maps (DesiredState, DeployedState,
// RuntimeState) are redacted so secret environment values are replaced with
// secrets.RedactedValue before serialization (docs/ACCORDA.md §21). Unknown
// payloads are returned unchanged; nil becomes nil so an event with no
// payload serializes as null.
func redactPayload(payload any) any {
	switch p := payload.(type) {
	case *health.Health:
		return redactHealth(p)
	case health.Health:
		return redactHealth(&p)
	case state.Comparison:
		return p // Comparison carries no secret values (Reasons/Result only)
	case reconcile.StateTransition:
		return redactTransition(p)
	case *state.DesiredState:
		return redactDesiredState(p)
	case *state.DeployedState:
		return redactDeployedState(p)
	case *state.RuntimeState:
		return redactRuntimeState(p)
	default:
		return payload
	}
}

// redactTransition copies a StateTransition with the error string rendered,
// since errors are not JSON-marshaled by default. The error string is passed
// through redactErrorText so any URL-embedded credentials in wrapped errors
// (for example a go-git clone failure that echoes the configured repository
// URL) are stripped before the payload is sent, since the Env redaction does
// not cover error strings (docs/ACCORDA.md §18, §56).
func redactTransition(t reconcile.StateTransition) map[string]any {
	out := map[string]any{
		"from":          string(t.From),
		"to":            string(t.To),
		"commit":        t.Commit,
		"deployment_id": t.DeploymentID,
	}
	if t.Err != nil {
		out["error"] = redactErrorText(t.Err.Error())
	}
	return out
}

// redactErrorText strips userinfo credentials from every URL embedded in an
// error message. It is defense-in-depth for error strings that reach a
// webhook payload, where a wrapped library error may echo a repository URL
// containing inline credentials even though Accorda's own error prefixes
// already use git.RedactURL. It handles both `scheme://user:pass@host/path`
// and `scheme://user@host/path` forms.
func redactErrorText(s string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, "://")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i+3]) // emit up to and including "://"
		tail := rest[i+3:]
		at := strings.Index(tail, "@")
		if at < 0 {
			// No userinfo on this URL; emit the rest unchanged.
			b.WriteString(tail)
			break
		}
		// A '/' before the '@' means the '@' is not part of the authority, so
		// there is no userinfo to strip.
		if slash := strings.Index(tail, "/"); slash >= 0 && slash < at {
			b.WriteString(tail)
			break
		}
		// Skip past the userinfo and the '@', then continue scanning the tail
		// for another URL.
		rest = tail[at+1:]
	}
	return b.String()
}

// redactHealth returns a JSON-safe copy of a health assessment. encoding/json
// sorts map keys on marshal, so the services map serializes deterministically
// (docs/DECISIONS.md #7).
func redactHealth(h *health.Health) any {
	if h == nil {
		return nil
	}
	services := make(map[string]any, len(h.Services))
	for name, sh := range h.Services {
		services[name] = map[string]any{
			"status":  string(sh.Status),
			"message": sh.Message,
		}
	}
	return map[string]any{
		"deployed": h.Deployed,
		"healthy":  h.Healthy,
		"overall":  string(h.Overall),
		"services": services,
		"checked":  h.CheckedAt.UTC().Format(time.RFC3339Nano),
	}
}

// redactDesiredState returns a JSON-safe copy of a desired state with every
// service's Env values replaced by secrets.RedactedValue. encoding/json sorts
// map keys on marshal, so the services map is deterministic
// (docs/DECISIONS.md #7).
func redactDesiredState(d *state.DesiredState) any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"repository":  d.Repository,
		"branch":      d.Branch,
		"commit":      d.Commit,
		"commit_time": d.CommitTime.UTC().Format(time.RFC3339Nano),
		"services":    redactServices(d.Services),
	}
}

// redactDeployedState returns a JSON-safe copy of a deployed state with every
// service's Env values redacted.
func redactDeployedState(d *state.DeployedState) any {
	if d == nil {
		return nil
	}
	return map[string]any{
		"deployment_id": d.DeploymentID,
		"commit":        d.Commit,
		"deployed_at":   d.DeployedAt.UTC().Format(time.RFC3339Nano),
		"services":      redactServices(d.Services),
	}
}

// redactRuntimeState returns a JSON-safe copy of a runtime state. Runtime
// services carry no Env.
func redactRuntimeState(r *state.RuntimeState) any {
	if r == nil {
		return nil
	}
	services := make(map[string]any, len(r.Services))
	for name, svc := range r.Services {
		services[name] = map[string]any{
			"status": svc.Status,
			"health": svc.Health,
			"image":  svc.Image,
			"digest": svc.Digest,
		}
	}
	return map[string]any{"services": services}
}

// redactServices builds a name-keyed map of redacted service descriptions.
// Each service's Env values are replaced with secrets.RedactedValue so
// secret environment variables never leave the process in a webhook payload
// (docs/ACCORDA.md §21).
func redactServices(services map[string]state.Service) map[string]any {
	out := make(map[string]any, len(services))
	for name, svc := range services {
		out[name] = map[string]any{
			"image":    svc.Image,
			"command":  svc.Command,
			"env":      redactEnv(svc.Env),
			"ports":    svc.Ports,
			"networks": svc.Networks,
		}
	}
	return out
}

// redactEnv returns a copy of env with every value replaced by
// secrets.RedactedValue, preserving keys so the receiver can see which
// variables are configured without learning their values.
func redactEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(env))
	for k := range env {
		out[k] = secrets.RedactedValue
	}
	return out
}

// signature computes the HMAC-SHA256 of body keyed by secret, returned as
// lowercase hex (docs/ACCORDA.md §21).
func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// retryable reports whether err should be retried. Network errors and 5xx
// (plus 429) responses are retryable; other HTTP status codes are not.
func retryable(err error) bool {
	var re retryableError
	if errors.As(err, &re) {
		if re.status == 0 {
			return true // network/transport failure
		}
		return re.status >= 500 || re.status == 429
	}
	return false
}

// retryableError wraps a transport error or non-2xx status code so retryable
// failures can be distinguished from non-retryable ones.
type retryableError struct {
	err    error
	status int
}

func (e retryableError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("webhook: HTTP %d", e.status)
}

func (e retryableError) Unwrap() error { return e.err }

// doubleBackoff doubles the retry delay, capping it at maxRetryBackoff.
func doubleBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxRetryBackoff {
		return maxRetryBackoff
	}
	return d
}

// sleepCtx sleeps for d, returning early with ctx.Err() when ctx is canceled
// or its deadline expires before d elapses.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
