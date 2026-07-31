package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RedactedJSON renders the configuration for human inspection with all
// secret-bearing material masked. The configuration layer never loads
// secret values, so nothing sensitive can leak here; masking remains as a
// defense in depth for future fields.
func (c *Config) RedactedJSON() ([]byte, error) {
	type redactedBackend struct {
		Type          string    `json:"type"`
		BaseURL       string    `json:"base_url,omitempty"`
		Model         string    `json:"model,omitempty"`
		CredentialEnv string    `json:"credential_env,omitempty"`
		Features      []string  `json:"features,omitempty"`
		Delay         *Duration `json:"delay,omitempty"`
		FailMode      string    `json:"fail_mode,omitempty"`
	}
	redacted := struct {
		Version      int                        `json:"version"`
		API          APIConfig                  `json:"api"`
		Database     DatabaseConfig             `json:"database"`
		Retention    RetentionConfig            `json:"retention"`
		Backends     map[string]redactedBackend `json:"backends"`
		Capabilities map[string]Capability      `json:"capabilities"`
		Workloads    map[string]WorkloadPolicy  `json:"workloads"`
	}{
		Version:      c.Version,
		API:          c.API,
		Database:     c.Database,
		Retention:    c.Retention,
		Backends:     make(map[string]redactedBackend, len(c.Backends)),
		Capabilities: c.Capabilities,
		Workloads:    c.Workloads,
	}
	for name, b := range c.Backends {
		rb := redactedBackend{
			Type:          b.Type,
			BaseURL:       b.BaseURL,
			Model:         b.Model,
			CredentialEnv: b.CredentialEnv,
			Features:      b.Features,
			FailMode:      b.FailMode,
		}
		if b.Delay.Duration > 0 {
			d := b.Delay
			rb.Delay = &d
		}
		// Mask credential references in redacted output: the operator
		// sees that a credential is configured, never its name, so
		// environment names cannot be leaked into logs.
		if rb.CredentialEnv != "" {
			rb.CredentialEnv = "***"
		}
		redacted.Backends[name] = rb
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&redacted); err != nil {
		return nil, fmt.Errorf("redact config: %w", err)
	}
	return buf.Bytes(), nil
}
