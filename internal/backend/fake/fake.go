// Package fake implements the deterministic built-in development adapter.
// It performs no network access and is the default backend for local and
// test deployments.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
)

func init() {
	backend.RegisterDefault(config.AdapterFake, New)
}

// Adapter is the deterministic fake inference backend.
type Adapter struct {
	delay    time.Duration
	failMode string
}

// New builds the fake adapter from operator configuration.
func New(cfg config.BackendConfig) (backend.Adapter, error) {
	return &Adapter{delay: cfg.Delay.Duration, failMode: cfg.FailMode}, nil
}

// Type returns "fake".
func (a *Adapter) Type() string { return config.AdapterFake }

// Infer produces a deterministic response or one of the configured fault
// modes. Cancellation and the configured delay are honored deterministically;
// a cancellation during the delay is classified as definitely-unsent because
// the fake performs no I/O.
func (a *Adapter) Infer(ctx context.Context, req *backend.Request) (*backend.Response, error) {
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
		case <-ctx.Done():
			return nil, backend.NewError(domain.ErrorTransientUnsent, "cancelled",
				"the inference was cancelled before it could be sent")
		}
	}

	switch a.failMode {
	case config.FakeFailNone:
		// deterministic content derived from the request
		h := sha256.Sum256([]byte(contentInput(req)))
		content := fmt.Sprintf("fake response %s", hex.EncodeToString(h[:8]))
		finish := domain.FinishStop
		if req.Response.Kind == domain.ResponseJSONObject || req.Response.Kind == domain.ResponseJSONSchema {
			content = "{}"
		}
		promptTokens := int64(0)
		for _, m := range req.Messages {
			promptTokens += int64(len(m.Content)+16) / 4
		}
		completion := int64(len(content)) / 4
		return &backend.Response{
			Content:      content,
			FinishReason: finish,
			Usage: domain.Usage{
				PromptTokens:     promptTokens,
				CompletionTokens: completion,
				TotalTokens:      promptTokens + completion,
			},
		}, nil
	case config.FakeFailReject:
		return nil, backend.NewError(domain.ErrorBackendRejected, "backend_rejected",
			"the fake backend rejected the request (fail_mode reject)")
	case config.FakeFailMalformed:
		return nil, backend.NewError(domain.ErrorOutputInvalid, "output_invalid",
			"the fake backend produced malformed output (fail_mode malformed)")
	case config.FakeFailUnsent:
		return nil, backend.NewError(domain.ErrorTransientUnsent, "connect_failed",
			"the fake backend could not be reached (fail_mode unsent)")
	case config.FakeFailTransient:
		return nil, backend.NewError(domain.ErrorTransientSafe, "backend_error",
			"the fake backend returned a transient error (fail_mode transient)")
	case config.FakeFailIndeterminate:
		return nil, backend.NewError(domain.ErrorIndeterminate, "indeterminate",
			"the fake backend disconnected ambiguously (fail_mode indeterminate)")
	default:
		return nil, backend.NewError(domain.ErrorInternal, "invalid_config",
			fmt.Sprintf("unknown fake fail_mode %q", a.failMode))
	}
}

// contentInput deterministically folds the normalized request into the
// response content seed: roles, message text, and generation options. No
// client identity or trace metadata participates.
func contentInput(req *backend.Request) string {
	var b []byte
	for _, m := range req.Messages {
		b = append(b, m.Role...)
		b = append(b, ':')
		b = append(b, m.Content...)
		b = append(b, '\n')
	}
	return string(b)
}
