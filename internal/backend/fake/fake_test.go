package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/backend/conformance"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
)

func newFake(t *testing.T, failMode string, delay time.Duration) *Adapter {
	t.Helper()
	a, err := New(config.BackendConfig{Type: config.AdapterFake, FailMode: failMode, Delay: config.Duration{Duration: delay}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a.(*Adapter)
}

func baseRequest() *backend.Request {
	return &backend.Request{
		Messages: []domain.Message{{Role: "user", Content: "hola"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	}
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func() backend.Adapter { return newFake(t, config.FakeFailNone, 0) })
}

func TestSuccessDeterministic(t *testing.T) {
	a := newFake(t, config.FakeFailNone, 0)
	r1, err := a.Infer(context.Background(), baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.Infer(context.Background(), baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if r1.Content != r2.Content {
		t.Errorf("non-deterministic content: %q vs %q", r1.Content, r2.Content)
	}
	if !strings.HasPrefix(r1.Content, "fake response ") {
		t.Errorf("unexpected content %q", r1.Content)
	}
	if r1.FinishReason != domain.FinishStop {
		t.Errorf("finish reason = %q, want stop", r1.FinishReason)
	}
	if r1.Usage.TotalTokens != r1.Usage.PromptTokens+r1.Usage.CompletionTokens {
		t.Errorf("usage inconsistent: %+v", r1.Usage)
	}
}

func TestStructuredKindsReturnObject(t *testing.T) {
	a := newFake(t, config.FakeFailNone, 0)
	for _, kind := range []domain.ResponseKind{domain.ResponseJSONObject, domain.ResponseJSONSchema} {
		req := baseRequest()
		req.Response.Kind = kind
		resp, err := a.Infer(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content != "{}" {
			t.Errorf("kind %s content = %q, want {}", kind, resp.Content)
		}
	}
}

func TestFailModes(t *testing.T) {
	cases := []struct {
		mode  string
		class domain.ErrorClass
	}{
		{config.FakeFailReject, domain.ErrorBackendRejected},
		{config.FakeFailMalformed, domain.ErrorOutputInvalid},
		{config.FakeFailUnsent, domain.ErrorTransientUnsent},
		{config.FakeFailTransient, domain.ErrorTransientSafe},
		{config.FakeFailIndeterminate, domain.ErrorIndeterminate},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			a := newFake(t, tc.mode, 0)
			_, err := a.Infer(context.Background(), baseRequest())
			be, ok := err.(*backend.Error)
			if !ok {
				t.Fatalf("error type %T, want *backend.Error", err)
			}
			if be.Class != tc.class {
				t.Errorf("class = %q, want %q", be.Class, tc.class)
			}
			if be.Code == "" || be.Message == "" {
				t.Errorf("error missing code/message: %+v", be)
			}
		})
	}
}

func TestDelayHonored(t *testing.T) {
	a := newFake(t, config.FakeFailNone, 80*time.Millisecond)
	start := time.Now()
	if _, err := a.Infer(context.Background(), baseRequest()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("elapsed %v, want >= 80ms", elapsed)
	}
}

func TestCancellationDuringDelay(t *testing.T) {
	a := newFake(t, config.FakeFailNone, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := a.Infer(ctx, baseRequest())
	be, ok := err.(*backend.Error)
	if !ok {
		t.Fatalf("error type %T, want *backend.Error", err)
	}
	if be.Class != domain.ErrorTransientUnsent {
		t.Errorf("class = %q, want transient_unsent (nothing was sent)", be.Class)
	}
}

func TestType(t *testing.T) {
	a := newFake(t, config.FakeFailNone, 0)
	if a.Type() != config.AdapterFake {
		t.Errorf("Type() = %q, want %q", a.Type(), config.AdapterFake)
	}
}
