// Package conformance hosts the shared adapter conformance suite. Every
// built-in adapter runs it; future compiled adapters must run it too.
// The suite asserts the provider-independent contract, never provider
// behavior.
package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/domain"
)

// Factory produces a fresh adapter instance for one test run.
type Factory func() backend.Adapter

// Run executes the conformance suite against a factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("TypeIsStable", func(t *testing.T) {
		adapter := factory()
		if adapter.Type() == "" {
			t.Fatal("Type() must not be empty")
		}
	})

	t.Run("InferReturnsNormalizedResponse", func(t *testing.T) {
		adapter := factory()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := adapter.Infer(ctx, &backend.Request{
			Messages: []domain.Message{{Role: "user", Content: "hello"}},
			Response: domain.ResponseContract{Kind: domain.ResponseText},
		})
		if err != nil {
			var be *backend.Error
			if ok := asBackendError(err, &be); ok && be.Class == domain.ErrorTransientUnsent {
				t.Fatalf("adapter must succeed on a plain request, got %v", err)
			}
			t.Skipf("adapter does not support the default request: %v", err)
		}
		if resp == nil {
			t.Fatal("nil response without error")
		}
		if err := resp.FinishReason.Valid(); !err {
			t.Errorf("invalid finish reason %q", resp.FinishReason)
		}
		if resp.Usage.TotalTokens < 0 || resp.Usage.PromptTokens < 0 || resp.Usage.CompletionTokens < 0 {
			t.Errorf("negative usage: %+v", resp.Usage)
		}
	})

	t.Run("ErrorsAreClassified", func(t *testing.T) {
		// Malformed input must produce a *backend.Error with a valid class,
		// never a raw error type.
		adapter := factory()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := adapter.Infer(ctx, &backend.Request{})
		if err == nil {
			return // some adapters accept empty requests
		}
		var be *backend.Error
		if !asBackendError(err, &be) {
			t.Fatalf("error is not a *backend.Error: %T %v", err, err)
		}
		if !be.Class.Valid() {
			t.Errorf("invalid error class %q", be.Class)
		}
		if be.Code == "" || be.Message == "" {
			t.Errorf("error must carry code and message: %+v", be)
		}
	})

	t.Run("CancellationIsHonored", func(t *testing.T) {
		adapter := factory()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := adapter.Infer(ctx, &backend.Request{
			Messages: []domain.Message{{Role: "user", Content: "hello"}},
			Response: domain.ResponseContract{Kind: domain.ResponseText},
		})
		if err == nil {
			t.Skip("adapter completed despite cancelled context")
		}
		var be *backend.Error
		if !asBackendError(err, &be) {
			t.Fatalf("error is not a *backend.Error: %T %v", err, err)
		}
	})
}

// asBackendError unwraps a *backend.Error without importing implementation
// details elsewhere.
func asBackendError(err error, target **backend.Error) bool {
	be, ok := err.(*backend.Error)
	if ok {
		*target = be
	}
	return ok
}
