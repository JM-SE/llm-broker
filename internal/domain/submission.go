package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// TaskSubmission is the normalized client-facing task request. Field names
// are part of the public API contract (see openapi.yaml).
type TaskSubmission struct {
	Workload   string           `json:"workload"`
	Capability string           `json:"capability"`
	Messages   []Message        `json:"messages"`
	Generation Generation       `json:"generation"`
	Response   ResponseContract `json:"response"`
	Trace      Trace            `json:"trace,omitempty"`
}

// DecodeTaskSubmission strictly decodes a request body and validates it.
// Unknown fields, duplicate fields, and invalid shapes are rejected; the
// resulting struct is the normalized form used for hashing and storage.
func DecodeTaskSubmission(r io.Reader) (*TaskSubmission, error) {
	dec := json.NewDecoder(io.LimitReader(r, MaxBodyBytes+1))
	dec.DisallowUnknownFields()
	var s TaskSubmission
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid task request: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks the intrinsic contract of a submission. Policy-level
// checks (grants, allowed generation fields, limits) happen at submission
// time against the workload policy.
func (s *TaskSubmission) Validate() error {
	if err := ValidateSlug("workload", s.Workload); err != nil {
		return err
	}
	if err := ValidateSlug("capability", s.Capability); err != nil {
		return err
	}
	if len(s.Messages) == 0 {
		return fmt.Errorf("messages must contain at least one message")
	}
	if len(s.Messages) > MaxMessages {
		return fmt.Errorf("messages has %d entries, max %d", len(s.Messages), MaxMessages)
	}
	total := 0
	for i, m := range s.Messages {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("messages[%d]: %w", i, err)
		}
		total += len(m.Content)
		if total > MaxCombinedMessageBytes {
			return fmt.Errorf("combined message content exceeds %d bytes", MaxCombinedMessageBytes)
		}
	}
	if err := s.Generation.Validate(); err != nil {
		return err
	}
	if err := s.Response.Validate(); err != nil {
		return err
	}
	if err := s.Trace.Validate(); err != nil {
		return err
	}
	return nil
}

// CanonicalBytes renders the deterministic canonical representation of the
// normalized submission: JSON with stable key ordering, normalized stop
// tokens, and canonicalized schema bytes. Two requests that are equivalent
// after normalization always produce identical bytes; the idempotency hash
// is derived from these bytes.
func (s *TaskSubmission) CanonicalBytes() ([]byte, error) {
	genBytes, err := s.Generation.Canonical()
	if err != nil {
		return nil, err
	}
	respBytes, err := s.Response.Canonical()
	if err != nil {
		return nil, err
	}
	canon := struct {
		Workload   string            `json:"workload"`
		Capability string            `json:"capability"`
		Messages   []Message         `json:"messages"`
		Generation json.RawMessage   `json:"generation,omitempty"`
		Response   json.RawMessage   `json:"response"`
		Trace      map[string]string `json:"trace,omitempty"`
	}{
		Workload:   s.Workload,
		Capability: s.Capability,
		Messages:   s.Messages,
		Generation: genBytes,
		Response:   respBytes,
		Trace:      s.Trace,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&canon); err != nil {
		return nil, fmt.Errorf("canonicalize request: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// RequestHash returns the hex SHA-256 of the canonical bytes, used as the
// deterministic idempotency fingerprint of a normalized request.
func (s *TaskSubmission) RequestHash() (string, error) {
	canon, err := s.CanonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}
