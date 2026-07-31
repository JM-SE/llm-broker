package domain

import (
	"time"
)

// ErrorClass is the stable broker error taxonomy. The classes are a frozen
// product contract; they map onto stable HTTP codes at the API boundary and
// onto retry decisions in the worker.
type ErrorClass string

const (
	// ErrorClientInvalid: malformed, oversized, or unknown client input.
	// Never retried.
	ErrorClientInvalid ErrorClass = "client_invalid"

	// ErrorPolicyRejected: the request is valid but the workload policy
	// rejects it (admission, disallowed generation field, over limits).
	// Never retried.
	ErrorPolicyRejected ErrorClass = "policy_rejected"

	// ErrorBackendRejected: the backend answered authoritatively that the
	// request was rejected (validation/format error). Never retried.
	ErrorBackendRejected ErrorClass = "backend_rejected"

	// ErrorOutputInvalid: the backend answered but the output failed
	// contract validation (schema mismatch, malformed payload). Never
	// auto-retried.
	ErrorOutputInvalid ErrorClass = "output_invalid"

	// ErrorTransientUnsent: the request was definitely not sent to the
	// backend (connection refused, dial timeout, pre-send failure). Safe
	// to auto-retry.
	ErrorTransientUnsent ErrorClass = "transient_unsent"

	// ErrorTransientSafe: a transient backend failure that provably did
	// not start an inference (e.g. backend answered before invoking a
	// model). Safe to auto-retry.
	ErrorTransientSafe ErrorClass = "transient_safe"

	// ErrorIndeterminate: connection loss or timeout while an inference
	// may have started; the outcome is unknown. Never auto-retried.
	ErrorIndeterminate ErrorClass = "indeterminate"

	// ErrorInternal: unexpected broker-side failure. Never auto-retried.
	ErrorInternal ErrorClass = "internal"
)

// Valid reports whether the class is one of the stable eight.
func (e ErrorClass) Valid() bool {
	switch e {
	case ErrorClientInvalid, ErrorPolicyRejected, ErrorBackendRejected,
		ErrorOutputInvalid, ErrorTransientUnsent, ErrorTransientSafe,
		ErrorIndeterminate, ErrorInternal:
		return true
	default:
		return false
	}
}

// Retryable reports whether the class is eligible for automatic retry.
// Only transient_unsent and transient_safe are ever auto-retried.
func (e ErrorClass) Retryable() bool {
	return e == ErrorTransientUnsent || e == ErrorTransientSafe
}

// RetryPolicy is the bounded retry configuration of a workload.
type RetryPolicy struct {
	MaxAttempts int             // total attempts including the first, 1..5
	BaseDelays  []time.Duration // delays between attempts (len <= MaxAttempts-1)
}
