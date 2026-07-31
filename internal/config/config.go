// Package config defines the strict versioned JSON configuration of the
// broker, its cross-reference validation, and its redacted inspection
// output.
//
// Configuration rules (product contract):
//   - Versioned JSON; unknown fields and unsupported versions fail.
//   - Secrets are never inline: database URL and backend credentials are
//     referenced by environment variable name only, and the loader never
//     reads those variables.
//   - Several backends may be declared, but global concurrency is fixed at
//     one inference and is not configurable in v1.
//   - Every capability resolves to exactly one configured backend.
//   - Backend URLs reject user info, fragments, invalid schemes, and
//     ambiguous forms.
//   - Validation errors identify the offending field without echoing
//     sensitive values.
package config

import (
	"fmt"
	"time"
)

// Version is the only supported configuration version.
const Version = 1

// Adapter types supported by the product. `fake` is the built-in
// development/test adapter; `openai_compatible` is the only real protocol
// adapter required by v1.
const (
	AdapterFake             = "fake"
	AdapterOpenAICompatible = "openai_compatible"
)

// Fake fail modes (development adapter behavior knobs).
const (
	FakeFailNone          = "none"
	FakeFailReject        = "reject"
	FakeFailMalformed     = "malformed"
	FakeFailUnsent        = "unsent"
	FakeFailTransient     = "transient"
	FakeFailIndeterminate = "indeterminate"
)

// Duration is a JSON string duration ("1h", "30s") parsed with
// time.ParseDuration.
type Duration struct{ time.Duration }

// UnmarshalJSON parses a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := jsonUnmarshalString(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalJSON renders the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return jsonMarshalString(d.Duration.String())
}

// Config is the validated runtime configuration.
type Config struct {
	Version      int                       `json:"version"`
	API          APIConfig                 `json:"api"`
	Database     DatabaseConfig            `json:"database"`
	Retention    RetentionConfig           `json:"retention"`
	Backends     map[string]BackendConfig  `json:"backends"`
	Capabilities map[string]Capability     `json:"capabilities"`
	Workloads    map[string]WorkloadPolicy `json:"workloads"`
}

// APIConfig configures the client-facing HTTP listener.
type APIConfig struct {
	// Listen is the host:port the API binds to. Loopback by default.
	Listen string `json:"listen"`
	// BodyLimitBytes bounds the request body (max MaxBodyBytes).
	BodyLimitBytes int64 `json:"body_limit_bytes,omitempty"`
}

// DatabaseConfig references the PostgreSQL connection by environment
// variable name; the value is never loaded by the config layer.
type DatabaseConfig struct {
	URLEnv                string `json:"url_env"`
	MaxConnections        int    `json:"max_connections,omitempty"`
	ConnectTimeoutSeconds int    `json:"connect_timeout_seconds,omitempty"`
}

// RetentionConfig bounds how long task data survives after terminal state.
// Ordering invariant: payload <= result <= metadata.
type RetentionConfig struct {
	Payload  Duration `json:"payload"`
	Result   Duration `json:"result"`
	Metadata Duration `json:"metadata"`
}

// BackendConfig declares one backend instance. Global concurrency stays at
// one inference regardless of how many backends are declared.
type BackendConfig struct {
	Type string `json:"type"`
	// BaseURL is required for openai_compatible. Rejects user info,
	// fragments, query strings, and non-http(s) schemes.
	BaseURL string `json:"base_url,omitempty"`
	// Model is the operator-chosen model alias for openai_compatible.
	Model string `json:"model,omitempty"`
	// CredentialEnv names the environment variable holding the backend
	// credential; only the name lives in configuration.
	CredentialEnv string `json:"credential_env,omitempty"`
	// Features declares adapter capabilities required by workloads.
	Features []string `json:"features,omitempty"`
	// Delay (fake only) simulates inference latency.
	Delay Duration `json:"delay,omitempty"`
	// FailMode (fake only) simulates failure classes.
	FailMode string `json:"fail_mode,omitempty"`
}

// Capability maps one logical capability to exactly one backend instance.
type Capability struct {
	Backend string `json:"backend"`
}

// WorkloadPolicy is the operator-defined policy for one workload slug.
type WorkloadPolicy struct {
	PriorityClass string `json:"priority_class"`
	// MaxActiveTasks bounds concurrent non-terminal tasks per client for
	// this workload (admission).
	MaxActiveTasks int `json:"max_active_tasks,omitempty"`
	// MaxAttempts is the retry budget including the first attempt.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// RetryDelaysSeconds are the base delays between attempts.
	RetryDelaysSeconds []int `json:"retry_delays_seconds,omitempty"`
	// MaxOutputTokens caps generated output (hard ceiling MaxOutputTokens).
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// AllowedGeneration restricts the generation vocabulary per field,
	// with optional narrower ranges. Absent map = all fields allowed with
	// intrinsic bounds.
	AllowedGeneration map[string]GenerationConstraint `json:"allowed_generation,omitempty"`
	// RequiredFeatures must be declared by the backend instance.
	RequiredFeatures []string `json:"required_features,omitempty"`
	// Retention optionally overrides the global retention within bounds.
	Retention *RetentionConfig `json:"retention,omitempty"`
}

// GenerationConstraint narrows one generation field for a workload.
type GenerationConstraint struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// MaxItems bounds the number of stop tokens.
	MaxItems *int `json:"max_items,omitempty"`
}
