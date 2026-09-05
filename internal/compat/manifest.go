// Package compat implements the descriptive, consumer-owned compatibility
// contract. It deliberately has no database, adapter, network, or credential
// dependencies.
package compat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JM-SE/llm-broker/internal/domain"
)

const (
	Version = "consumer-compat/v1"

	// CurrentAPIVersion is the public API version covered by H03. It is kept
	// here rather than in the manifest so a manifest cannot choose an API.
	CurrentAPIVersion = "0.1.1"

	maxSemverPart = 1000000
)

// Response describes the output shape requested by a consumer. The optional
// schema_required flag is retained in the normalized form when it was
// explicitly supplied.
type Response struct {
	Kind           domain.ResponseKind `json:"kind"`
	SchemaRequired bool                `json:"schema_required,omitempty"`
	schemaSet      bool
}

// Manifest is the normalized consumer-compat/v1 document. It contains only
// public request and compatibility requirements; it is not authorization
// material and cannot describe a provider or topology.
type Manifest struct {
	Version                               string            `json:"version"`
	APIRange                              string            `json:"api_range"`
	Workload                              string            `json:"workload"`
	Capability                            string            `json:"capability"`
	Generation                            domain.Generation `json:"generation"`
	Response                              Response          `json:"response"`
	MaxRequestContentBytes                int               `json:"max_request_content_bytes"`
	RequiredMinOutputBytes                int               `json:"required_min_output_bytes"`
	MinWorkloadTimeoutSeconds             int               `json:"min_workload_timeout_seconds"`
	MinBackendResponseStartTimeoutSeconds int               `json:"min_backend_response_start_timeout_seconds"`
	MaxAcceptableAttempts                 int               `json:"max_acceptable_attempts"`
	ConsumerOverallDeadlineSeconds        int               `json:"consumer_overall_deadline_seconds"`
}

// Validate applies semantic checks to a decoded manifest. Parse should be
// preferred at untrusted boundaries because it also enforces structural
// bounds and duplicate-key rejection.
func (m *Manifest) Validate() error {
	if m == nil {
		return fieldError("manifest", "is required")
	}
	if m.Version != Version {
		return fieldError("version", "is unsupported")
	}
	if _, err := parseRange(m.APIRange); err != nil {
		return fieldError("api_range", "is invalid")
	}
	if err := domain.ValidateSlug("workload", m.Workload); err != nil {
		return fieldError("workload", "is invalid")
	}
	if err := domain.ValidateSlug("capability", m.Capability); err != nil {
		return fieldError("capability", "is invalid")
	}
	if err := m.Generation.Validate(); err != nil {
		return fieldError("generation", "is invalid")
	}
	for _, name := range m.Generation.PresentFields() {
		switch name {
		case "max_tokens":
			if m.Generation.MaxTokens == nil {
				return fieldError("generation.max_tokens", "must not be null")
			}
		case "temperature":
			if m.Generation.Temperature == nil {
				return fieldError("generation.temperature", "must not be null")
			}
		case "top_p":
			if m.Generation.TopP == nil {
				return fieldError("generation.top_p", "must not be null")
			}
		case "top_k":
			if m.Generation.TopK == nil {
				return fieldError("generation.top_k", "must not be null")
			}
		case "min_p":
			if m.Generation.MinP == nil {
				return fieldError("generation.min_p", "must not be null")
			}
		case "seed":
			if m.Generation.Seed == nil {
				return fieldError("generation.seed", "must not be null")
			}
		case "stop":
			if m.Generation.Stop == nil {
				return fieldError("generation.stop", "must not be null")
			}
		}
	}
	if !m.Response.Kind.Valid() {
		return fieldError("response.kind", "is unsupported")
	}
	if m.Response.SchemaRequired && m.Response.Kind != domain.ResponseJSONSchema {
		return fieldError("response.schema_required", "requires response.kind json_schema")
	}
	if m.MaxRequestContentBytes < 1 || m.RequiredMinOutputBytes < 1 ||
		m.MinWorkloadTimeoutSeconds < 0 || m.MinBackendResponseStartTimeoutSeconds < 0 ||
		m.MaxAcceptableAttempts < 1 || m.ConsumerOverallDeadlineSeconds < 1 {
		return fieldError("limits", "contains an out-of-range value")
	}
	return nil
}

// Canonical returns the deterministic semantic JSON representation used for
// the manifest digest. Struct order is fixed, while generation normalizes
// stop-token ordering and duplicate stop tokens.
func (m Manifest) Canonical() ([]byte, error) {
	gen, err := m.Generation.Canonical()
	if err != nil {
		return nil, err
	}
	response := struct {
		Kind           domain.ResponseKind `json:"kind"`
		SchemaRequired *bool               `json:"schema_required,omitempty"`
	}{Kind: m.Response.Kind}
	if m.Response.schemaSet {
		value := m.Response.SchemaRequired
		response.SchemaRequired = &value
	}
	canonical := struct {
		Version                               string          `json:"version"`
		APIRange                              string          `json:"api_range"`
		Workload                              string          `json:"workload"`
		Capability                            string          `json:"capability"`
		Generation                            json.RawMessage `json:"generation"`
		Response                              json.RawMessage `json:"response"`
		MaxRequestContentBytes                int             `json:"max_request_content_bytes"`
		RequiredMinOutputBytes                int             `json:"required_min_output_bytes"`
		MinWorkloadTimeoutSeconds             int             `json:"min_workload_timeout_seconds"`
		MinBackendResponseStartTimeoutSeconds int             `json:"min_backend_response_start_timeout_seconds"`
		MaxAcceptableAttempts                 int             `json:"max_acceptable_attempts"`
		ConsumerOverallDeadlineSeconds        int             `json:"consumer_overall_deadline_seconds"`
	}{
		Version: m.Version, APIRange: normalizeRange(m.APIRange), Workload: m.Workload,
		Capability: m.Capability, Generation: gen,
		MaxRequestContentBytes:                m.MaxRequestContentBytes,
		RequiredMinOutputBytes:                m.RequiredMinOutputBytes,
		MinWorkloadTimeoutSeconds:             m.MinWorkloadTimeoutSeconds,
		MinBackendResponseStartTimeoutSeconds: m.MinBackendResponseStartTimeoutSeconds,
		MaxAcceptableAttempts:                 m.MaxAcceptableAttempts,
		ConsumerOverallDeadlineSeconds:        m.ConsumerOverallDeadlineSeconds,
	}
	canonicalResponse, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	canonical.Response = canonicalResponse
	// SetEscapeHTML(false) keeps the digest stable across languages: the
	// api_range value legitimately contains < and >, and a consumer-side
	// canonicalizer must be able to reproduce these exact bytes without
	// replicating Go's implicit HTML escaping.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&canonical); err != nil {
		return nil, fmt.Errorf("canonicalize manifest: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// Digest returns the semantic SHA-256 digest of the normalized manifest.
func (m Manifest) Digest() ([32]byte, error) {
	canonical, err := m.Canonical()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

// DigestString is the safe CLI representation of Digest.
func (m Manifest) DigestString() (string, error) {
	digest, err := m.Digest()
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func fieldError(field, reason string) error { return fmt.Errorf("manifest.%s %s", field, reason) }

func normalizeRange(raw string) string { return strings.Join(strings.Fields(raw), " ") }
