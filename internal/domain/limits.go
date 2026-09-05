// Package domain defines the provider-independent core types, limits,
// state machine, and canonical request representation of the broker.
//
// The domain package contains no configuration, persistence, networking,
// or provider-specific concepts. All identifiers and values defined here
// are frozen contracts of the public v1 product.
package domain

// Global request and output limits. These are product invariants: they are
// enforced at the API boundary and are not configurable in v1.
const (
	// MaxMessages is the maximum number of messages in one task request.
	MaxMessages = 64

	// MaxCombinedMessageBytes is the maximum total size of all message
	// content in one task request, in bytes.
	MaxCombinedMessageBytes = 256 * 1024

	// MaxBodyBytes is the maximum HTTP request body size, in bytes.
	MaxBodyBytes = 512 * 1024

	// MaxTraceBytes is the maximum serialized size of trace metadata.
	MaxTraceBytes = 4 * 1024

	// MaxTraceKeys is the maximum number of trace metadata keys.
	MaxTraceKeys = 16

	// MaxTraceValueBytes is the maximum size of one trace value.
	MaxTraceValueBytes = 256

	// MaxSchemaBytes is the maximum size of a client-provided JSON Schema.
	MaxSchemaBytes = 64 * 1024

	// MaxSchemaDepth is the maximum nesting depth of a client-provided
	// JSON Schema.
	MaxSchemaDepth = 16

	// MaxSchemaProperties is the maximum number of declared properties in
	// any single object within a client-provided JSON Schema.
	MaxSchemaProperties = 256

	// MaxOutputTokens is the global hard output limit in tokens. Workload
	// policies may reduce this limit but never exceed it.
	MaxOutputTokens = 8192

	// MaxOutputBytes is the global hard UTF-8 content limit. Workload policies
	// may reduce it; they may not exceed it.
	MaxOutputBytes = 256 * 1024

	// MinIdempotencyKeyLen and MaxIdempotencyKeyLen bound the
	// Idempotency-Key header used at the API boundary.
	MinIdempotencyKeyLen = 8
	MaxIdempotencyKeyLen = 200

	// DefaultMaxAttempts is the default retry budget: the first attempt
	// plus two retries.
	DefaultMaxAttempts = 3

	// DefaultRetryDelaySeconds and DefaultRetryDelaySeconds2 are the base
	// retry delays applied by the worker (subject to bounded jitter).
	DefaultRetryDelaySeconds  = 1
	DefaultRetryDelaySeconds2 = 5
)
