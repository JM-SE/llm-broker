// Package backend defines the provider-independent inference adapter
// contract, the optional runtime-controller contract, and the stable
// feature declarations.
//
// Design rules:
//   - Adapters translate the normalized domain request into a provider
//     protocol; provider-specific fields never leak into the domain model.
//   - The optional Controller is a separate, provider-specific runtime
//     management helper used ONLY to prove backend idleness after an
//     ambiguous disconnect or to fence a backend. Generic OpenAI-compatible
//     deployments may have no controller and fail closed until operator
//     recovery.
//   - No adapter may initiate network behavior at construction; adapters
//     are inert factories until the worker invokes them.
package backend

import (
	"context"
	"fmt"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// Feature is a capability a backend instance may declare and a workload
// policy may require.
type Feature string

const (
	// FeatureStructuredOutput: the adapter can constrain output to a
	// JSON Schema / JSON object contract.
	FeatureStructuredOutput Feature = "structured_output"
)

// Valid reports whether the feature is known.
func (f Feature) Valid() bool {
	switch f {
	case FeatureStructuredOutput:
		return true
	default:
		return false
	}
}

// ParseFeature parses a feature name.
func ParseFeature(s string) (Feature, error) {
	f := Feature(s)
	if !f.Valid() {
		return "", fmt.Errorf("unknown adapter feature %q", s)
	}
	return f, nil
}

// Request is the normalized inference request handed to adapters. It
// contains no client identity, no priority, no model selection, and no
// provider-specific fields.
type Request struct {
	Messages   []domain.Message
	Generation domain.Generation
	Response   domain.ResponseContract
}

// Response is the normalized inference response. Hidden reasoning content
// is discarded by the adapter and must never appear here, in logs, or in
// persistence.
type Response struct {
	Content      string
	FinishReason domain.FinishReason
	Usage        domain.Usage
}

// Error is the classified adapter failure. Class follows the stable
// broker taxonomy; Code is a stable machine-readable identifier; Message
// is a sanitized human description that never echoes prompts or provider
// error bodies.
type Error struct {
	Class   domain.ErrorClass
	Code    string
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// IsIndeterminate reports whether the error is of indeterminate class.
func (e *Error) IsIndeterminate() bool { return e.Class == domain.ErrorIndeterminate }

// NewError builds a classified adapter error.
func NewError(class domain.ErrorClass, code, message string) *Error {
	return &Error{Class: class, Code: code, Message: message}
}

// Adapter is the inference protocol adapter contract. Implementations are
// registered by adapter type name and constructed from operator
// configuration at worker startup. Exactly one Infer call is active
// globally while the worker holds the execution lease.
type Adapter interface {
	// Type returns the adapter type name (for example "fake" or
	// "openai_compatible").
	Type() string
	// Infer performs one bounded inference. The returned error is always
	// a *Error with a stable class, or a plain error that callers treat
	// as internal.
	Infer(ctx context.Context, req *Request) (*Response, error)
}

// Controller is the optional provider-specific runtime helper. It is
// strictly separated from Adapter: a deployment may run an OpenAI-
// compatible adapter with no controller and fail closed on ambiguous
// outcomes, pending operator recovery.
type Controller interface {
	// Type returns the controller type name.
	Type() string
	// Idle reports whether the backend runtime is provably idle (no
	// inference in flight). False is never a proof that an inference is
	// running; true is only usable when the controller has authoritative
	// evidence (for example a runtime-reported generation state).
	Idle(ctx context.Context) (bool, error)
	// Fence renders the backend unable to start new inferences (for
	// example by denying the broker credential or disabling the model),
	// used before reclaiming an expired lease.
	Fence(ctx context.Context) error
}

// Check is a compile-time assertion that Request/Response carry the
// normalized domain types.
var (
	_ = Request{}
	_ = Response{}
	_ = Error{}
)
