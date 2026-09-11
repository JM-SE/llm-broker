package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/backend/conformance"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
)

const testCredential = "test-secret-token-42"

func newAdapter(t *testing.T, baseURL string, features ...string) *Adapter {
	t.Helper()
	t.Setenv("LLM_BROKER_TEST_BACKEND_KEY", testCredential)
	a, err := New(config.BackendConfig{
		Type:          config.AdapterOpenAICompatible,
		BaseURL:       baseURL,
		Model:         "operator-model-alias",
		CredentialEnv: "LLM_BROKER_TEST_BACKEND_KEY",
		Features:      features,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a.(*Adapter)
}

func okHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testCredential {
			t.Errorf("Authorization = %q, want bearer test credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hi there"}}],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`)
	}
}

func TestConformance(t *testing.T) {
	srv := httptest.NewServer(okHandler(t))
	defer srv.Close()
	conformance.Run(t, func() backend.Adapter { return newAdapter(t, srv.URL) })
}

func TestConfiguredResponseHeaderTimeout(t *testing.T) {
	t.Setenv("LLM_BROKER_TEST_BACKEND_KEY", testCredential)
	a, err := New(config.BackendConfig{
		Type: config.AdapterOpenAICompatible, BaseURL: "http://runtime.example/v1",
		Model: "model", CredentialEnv: "LLM_BROKER_TEST_BACKEND_KEY",
		ResponseHeaderTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := a.(*Adapter)
	if adapter.respHeaderTimeout != 60*time.Second {
		t.Fatalf("adapter timeout = %v, want 1m", adapter.respHeaderTimeout)
	}
	transport, ok := adapter.client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 60*time.Second {
		t.Fatalf("transport timeout = %v, want 1m", transport.ResponseHeaderTimeout)
	}
}

func TestGoldenRequestMapping(t *testing.T) {
	var gotBody string
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{}}`)
	}))
	defer srv.Close()

	a := newAdapter(t, srv.URL)
	maxTokens := int64(128)
	temp := 0.7
	topP := 0.95
	topK := int64(40)
	minP := 0.05
	seed := int64(7)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hello"},
		},
		Generation: domain.Generation{
			MaxTokens: &maxTokens, Temperature: &temp, TopP: &topP,
			TopK: &topK, MinP: &minP, Seed: &seed, Stop: []string{"END", "bye"},
		},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer "+testCredential {
		t.Errorf("authorization = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if body["model"] != "operator-model-alias" {
		t.Errorf("model = %v", body["model"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %v", body["messages"])
	}
	if body["max_tokens"] != float64(128) || body["temperature"] != 0.7 ||
		body["top_p"] != 0.95 || body["top_k"] != float64(40) || body["min_p"] != 0.05 ||
		body["seed"] != float64(7) {
		t.Errorf("generation mapping wrong: %v", body)
	}
	stop, ok := body["stop"].([]any)
	if !ok || len(stop) != 2 {
		t.Errorf("stop = %v", body["stop"])
	}
	// The mapping must never invent unsupported or private fields.
	for _, banned := range []string{"frequency_penalty", "presence_penalty", "response_format", "n", "stream", "user", "tools"} {
		if _, ok := body[banned]; ok {
			t.Errorf("request body must not contain %q: %v", banned, gotBody)
		}
	}
}

func TestStructuredOutputOnlyWhenDeclared(t *testing.T) {
	t.Run("declared", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			gotBody = string(buf)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{}}`)
		}))
		defer srv.Close()
		a := newAdapter(t, srv.URL, "structured_output")
		_, err := a.Infer(context.Background(), &backend.Request{
			Messages: []domain.Message{{Role: "user", Content: "x"}},
			Response: domain.ResponseContract{Kind: domain.ResponseJSONObject},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(gotBody, `"response_format":{"type":"json_object"}`) {
			t.Errorf("missing response_format: %s", gotBody)
		}
	})
	t.Run("not declared", func(t *testing.T) {
		srv := httptest.NewServer(okHandler(t))
		defer srv.Close()
		a := newAdapter(t, srv.URL)
		_, err := a.Infer(context.Background(), &backend.Request{
			Messages: []domain.Message{{Role: "user", Content: "x"}},
			Response: domain.ResponseContract{Kind: domain.ResponseJSONObject},
		})
		be, ok := err.(*backend.Error)
		if !ok || be.Class != domain.ErrorInternal || be.Code != "feature_not_declared" {
			t.Errorf("error = %v, want feature_not_declared", err)
		}
	})
}

func TestReasoningContentDiscarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"visible answer","reasoning_content":"secret chain of thought"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	resp, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "visible answer" {
		t.Errorf("content = %q", resp.Content)
	}
	if strings.Contains(resp.Content, "secret chain") {
		t.Error("reasoning leaked into content")
	}
	if resp.Usage.CompletionTokens != 1 || resp.Usage.TotalTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		class  domain.ErrorClass
		code   string
	}{
		{"bad request", 400, domain.ErrorBackendRejected, "backend_rejected"},
		{"unauthorized", 401, domain.ErrorBackendRejected, "backend_rejected"},
		{"not found", 404, domain.ErrorBackendRejected, "backend_rejected"},
		{"too many", 429, domain.ErrorTransientSafe, "backend_error"},
		{"server error", 500, domain.ErrorTransientSafe, "backend_error"},
		{"bad gateway", 502, domain.ErrorTransientSafe, "backend_error"},
		{"redirect", 301, domain.ErrorBackendRejected, "unexpected_status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status >= 300 && tc.status < 400 {
					http.Redirect(w, r, "https://evil.example/", http.StatusMovedPermanently)
					return
				}
				http.Error(w, "provider detail that must never surface", tc.status)
			}))
			defer srv.Close()
			a := newAdapter(t, srv.URL)
			_, err := a.Infer(context.Background(), &backend.Request{
				Messages: []domain.Message{{Role: "user", Content: "x"}},
				Response: domain.ResponseContract{Kind: domain.ResponseText},
			})
			be, ok := err.(*backend.Error)
			if !ok {
				t.Fatalf("error type %T, want *backend.Error", err)
			}
			if be.Class != tc.class || be.Code != tc.code {
				t.Errorf("error = %s/%s, want %s/%s", be.Class, be.Code, tc.class, tc.code)
			}
			if strings.Contains(be.Message, "must never surface") {
				t.Error("raw provider body leaked into the sanitized error")
			}
		})
	}
}

func TestContextExceededResponseHasStableCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"request (18672 tokens) exceeds the available context size (16384 tokens)"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorBackendRejected || be.Code != "backend_context_exceeded" {
		t.Fatalf("error = %v, want backend_rejected/backend_context_exceeded", err)
	}
	if strings.Contains(be.Message, "18672") || strings.Contains(be.Message, "16384") {
		t.Fatalf("context details leaked into message: %q", be.Message)
	}
}

func TestContextClassificationIsBoundedAndConservative(t *testing.T) {
	contextMessage := []byte("request (18672 tokens) exceeds the available context size (16384 tokens)")

	if got := classifyHTTPStatus(http.StatusBadRequest, []byte("ordinary rejection")); got.Code != "backend_rejected" {
		t.Fatalf("generic 400 code = %q, want backend_rejected", got.Code)
	}
	if got := classifyHTTPStatus(http.StatusTooManyRequests, contextMessage); got.Code != "backend_error" || got.Class != domain.ErrorTransientSafe {
		t.Fatalf("429 context code/class = %s/%s, want backend_error/transient_safe", got.Code, got.Class)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxErrorBodyBytes))
		_, _ = w.Write(contextMessage)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Code != "backend_rejected" {
		t.Fatalf("truncated context code = %v, want backend_rejected", err)
	}
}

func TestMalformedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{definitely not json`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorOutputInvalid || be.Code != "malformed_response" {
		t.Errorf("error = %v, want output_invalid/malformed_response", err)
	}
}

func TestOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"`)
		chunk := strings.Repeat("x", 64<<10)
		for i := 0; i < 80; i++ { // 5 MiB total
			fmt.Fprint(w, chunk)
		}
		fmt.Fprint(w, `"}}]}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorOutputInvalid || be.Code != "response_too_large" {
		t.Errorf("error = %v, want output_invalid/response_too_large", err)
	}
}

func TestConnectRefusedIsUnsent(t *testing.T) {
	// Dial a closed loopback port: provably nothing was sent.
	a := newAdapter(t, "http://127.0.0.1:1")
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorTransientUnsent || be.Code != "connect_failed" {
		t.Errorf("error = %v, want transient_unsent/connect_failed", err)
	}
}

func TestSlowServerTimesOutAsIndeterminate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"late"}}],"usage":{}}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	a.setResponseHeaderTimeout(30 * time.Millisecond)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorIndeterminate {
		t.Errorf("error = %v, want indeterminate (request was accepted)", err)
	}
}

func TestDisconnectMidBodyIsIndeterminate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"par`)
		fl, ok := w.(http.Flusher)
		if ok {
			fl.Flush()
		}
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	_, err := a.Infer(context.Background(), &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorIndeterminate {
		t.Errorf("error = %v, want indeterminate", err)
	}
}

func TestCancellationIsIndeterminate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := a.Infer(ctx, &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "x"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	be, ok := err.(*backend.Error)
	if !ok || be.Class != domain.ErrorIndeterminate {
		t.Errorf("error = %v, want indeterminate", err)
	}
}

func TestMissingCredentialEnvFailsConstruction(t *testing.T) {
	t.Setenv("LLM_BROKER_MISSING_KEY", "")
	_, err := New(config.BackendConfig{
		Type:          config.AdapterOpenAICompatible,
		BaseURL:       "http://127.0.0.1:1",
		Model:         "m",
		CredentialEnv: "LLM_BROKER_MISSING_KEY",
	})
	if err == nil {
		t.Fatal("expected error when credential env is empty")
	}
}

func TestEndpointJoining(t *testing.T) {
	for _, base := range []string{"http://example.test/v1", "http://example.test/v1/"} {
		a := newAdapter(t, base)
		if got := a.endpoint(); got != "http://example.test/v1/chat/completions" {
			t.Errorf("endpoint(%s) = %q", base, got)
		}
	}
}
