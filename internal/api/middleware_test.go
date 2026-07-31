package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequestIDPresent pins the correlation id header.
func TestRequestIDPresent(t *testing.T) {
	env := newTestEnv(t)
	resp, _ := doReq(t, env.ts, http.MethodGet, "/health", "", "", "", "")
	id := resp.Header.Get("X-Request-Id")
	if len(id) != 12 {
		t.Errorf("X-Request-Id = %q, want 12 hex chars", id)
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("X-Request-Id not hex: %q", id)
		}
	}
}

// TestRecoveryMiddleware converts a panic into a sanitized 500.
func TestRecoveryMiddleware(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := requestID(recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), logger))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), codeInternal) {
		t.Errorf("body missing stable code: %s", rec.Body.String())
	}
	// The panic details stay server-side, in logs.
	if !strings.Contains(logs.String(), "boom") {
		t.Error("panic cause missing from server logs")
	}
}

// TestLogsSanitized pins that request logs never contain credentials,
// idempotency keys, or message content.
func TestLogsSanitized(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "logcheck")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	body := `{"workload": "` + wl + `", "capability": "instruct",
		"messages": [{"role": "user", "content": "super-secret-prompt-42"}],
		"response": {"kind": "text"}}`
	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "log-key-001", "", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create failed: %d", resp.StatusCode)
	}

	logs := env.logs.String()
	for _, banned := range []string{tok.Value, "log-key-001", "super-secret-prompt-42"} {
		if strings.Contains(logs, banned) {
			t.Errorf("logs leak %q: %s", banned, logs)
		}
	}
	for _, want := range []string{"request_id=", "method=POST", "route=tasks.create", "status=201"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q: %s", want, logs)
		}
	}
}
