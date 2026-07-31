package api

import (
	"net/http"
	"strings"
	"testing"
)

// newMetricsEnv is newTestEnv with metrics exposure controlled by the flag.
func newMetricsEnv(t *testing.T, metricsEnabled bool) *testEnv {
	t.Helper()
	cfg := testConfig()
	cfg.API.MetricsEnabled = metricsEnabled
	env := newTestEnvWithConfig(t, cfg)
	return env
}

// TestMetricsEndpointEnabled verifies GET /metrics is served only when the
// operator enables api.metrics_enabled.
func TestMetricsEndpointEnabled(t *testing.T) {
	disabled := newMetricsEnv(t, false)
	resp, _ := doReq(t, disabled.ts, http.MethodGet, "/metrics", "", "", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("metrics disabled: status = %d, want 404", resp.StatusCode)
	}

	enabled := newMetricsEnv(t, true)
	// Warm up: a completed request registers the counter family (the scrape
	// itself is counted only after the response is served).
	doReq(t, enabled.ts, http.MethodGet, "/health", "", "", "", "")
	resp, body := doReq(t, enabled.ts, http.MethodGet, "/metrics", "", "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics enabled: status = %d, want 200 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "llm_broker_http_requests_total") {
		t.Errorf("metrics output missing request counter")
	}
}

// TestMetricsOutputNoSensitiveValues exercises the API with distinctive
// canary values and verifies none of them reach the metrics scrape: no
// tokens, keys, prompts, client names, or task identifiers.
func TestMetricsOutputNoSensitiveValues(t *testing.T) {
	env := newMetricsEnv(t, true)
	tok, clientID := newClient(t, "metrics-client")
	workload := grantWorkload(t, clientID, 10)

	const (
		promptCanary = "canary-prompt-4242"
		keyCanary    = "canary-key-4242"
	)
	body := strings.Replace(submission(workload), "Hello", promptCanary, 1)

	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, keyCanary, "", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}

	_, scraped := doReq(t, env.ts, http.MethodGet, "/metrics", "", "", "", "")
	text := string(scraped)
	for _, banned := range []string{
		promptCanary, keyCanary, tok.Value, tok.Identifier,
		"metrics-client", "canary",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("metrics output leaks %q", banned)
		}
	}
	if !strings.Contains(text, `path="tasks.create"`) {
		t.Errorf("metrics output missing bounded route label tasks.create")
	}
	if strings.Contains(text, "/v1/tasks/") {
		t.Errorf("metrics output contains a raw task path")
	}
}

// TestMetricsCardinalityBoundedUnderUniqueClients submits tasks from many
// distinct clients and keys, then verifies the request counter series stays
// within the fixed route/status vocabulary (client identity never becomes a
// label).
func TestMetricsCardinalityBoundedUnderUniqueClients(t *testing.T) {
	env := newMetricsEnv(t, true)
	names := map[string]string{}
	for i := 0; i < 5; i++ {
		tok, clientID := newClient(t, "card-client")
		workload := grantWorkload(t, clientID, 10)
		names[tok.Identifier] = tok.Value
		doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "card-key-"+tok.Identifier, "", submission(workload))
	}

	_, body := doReq(t, env.ts, http.MethodGet, "/metrics", "", "", "", "")
	text := string(body)

	// The counter series is bounded by method x route x status; 5 clients
	// must not multiply the number of series.
	const maxSeries = 16
	n := strings.Count(text, "llm_broker_http_requests_total{")
	if n > maxSeries {
		t.Errorf("request counter series = %d, want <= %d", n, maxSeries)
	}
	for id, token := range names {
		if strings.Contains(text, id) || strings.Contains(text, token) {
			t.Errorf("metrics output leaks client credential material")
		}
	}
	if strings.Contains(text, "card-client") {
		t.Errorf("metrics output leaks client names")
	}
}
