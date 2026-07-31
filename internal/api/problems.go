// Package api implements the client-facing HTTP surface of the broker:
// authentication, admission, durable task creation with optional bounded
// wait, scoped reads, cancellation intent, health, and readiness. It never
// schedules or executes inference; those responsibilities belong to the
// worker. Every error is an RFC 7807 problem document with a stable
// machine-readable code (see openapi.yaml).
package api

import (
	"encoding/json"
	"net/http"
)

// Stable machine-readable error codes. These identifiers are part of the
// public contract and must not change between releases.
const (
	codeMalformedRequest    = "malformed_request"
	codeUnprocessable       = "unprocessable"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeNotFound            = "not_found"
	codeConflict            = "conflict"
	codeIdempotencyConflict = "idempotency_conflict"
	codeAdmissionLimited    = "admission_limited"
	codeUnavailable         = "unavailable"
	codeInternal            = "internal_error"
)

// problemType follows RFC 7807: no dedicated problem-document URI.
const problemType = "about:blank"

// problem is the RFC 7807 response body. detail is optional and must never
// carry sensitive or provider-specific material.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code"`
}

func writeProblem(w http.ResponseWriter, status int, code, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type: problemType, Title: title, Status: status, Detail: detail, Code: code,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
