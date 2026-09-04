package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
)

// Known adapter features declared by backend instances.
const (
	FeatureStructuredOutput = "structured_output"
)

var knownFeatures = map[string]bool{
	FeatureStructuredOutput: true,
}

var envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Validate cross-checks the configuration after defaults are applied.
// Error messages identify the offending field but never echo secret
// values (the configuration layer never loads them).
func (c *Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d (supported: %d); convert configuration to v2, run migration and policy apply during planned downtime, then restart", c.Version, Version)
	}
	if err := c.validateAPI(); err != nil {
		return err
	}
	if err := c.validateDatabase(); err != nil {
		return err
	}
	if err := c.validateRetention(c.Retention, "retention"); err != nil {
		return err
	}
	if err := c.validateBackends(); err != nil {
		return err
	}
	if err := c.validateCapabilities(); err != nil {
		return err
	}
	if err := c.validateWorkloads(); err != nil {
		return err
	}
	return nil
}

func (c *Config) validateAPI() error {
	host, portStr, err := net.SplitHostPort(c.API.Listen)
	if err != nil {
		return fmt.Errorf("api.listen %q is not a valid host:port: %v", c.API.Listen, err)
	}
	if host == "" {
		return fmt.Errorf("api.listen %q must include a host", c.API.Listen)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("api.listen %q has an invalid port %q", c.API.Listen, portStr)
	}
	if c.API.BodyLimitBytes < 1024 || c.API.BodyLimitBytes > domain.MaxBodyBytes {
		return fmt.Errorf("api.body_limit_bytes %d outside [1024, %d]", c.API.BodyLimitBytes, domain.MaxBodyBytes)
	}
	return nil
}

func (c *Config) validateDatabase() error {
	roles := []struct {
		field string
		name  string
	}{
		{"database.url_env", c.Database.URLEnv},
		{"database.provisioner_url_env", c.Database.ProvisionerURLEnv},
		{"database.migration_url_env", c.Database.MigrationURLEnv},
	}
	seen := make(map[string]string, len(roles))
	for _, role := range roles {
		if role.name == "" {
			return fmt.Errorf("%s must name a distinct environment variable for its database role", role.field)
		}
		if !envNameRe.MatchString(role.name) {
			return fmt.Errorf("%s %q is not a valid environment variable name", role.field, role.name)
		}
		if previous, ok := seen[role.name]; ok {
			return fmt.Errorf("%s must differ from %s", role.field, previous)
		}
		seen[role.name] = role.field
	}
	if len(seen) != len(roles) {
		return fmt.Errorf("database role environment variables must be distinct")
	}
	for field, name := range map[string]string{
		"database.provisioner_url_env": c.Database.ProvisionerURLEnv,
		"database.migration_url_env":   c.Database.MigrationURLEnv,
	} {
		if name == "" || !envNameRe.MatchString(name) {
			return fmt.Errorf("%s %q is not a valid environment variable name", field, name)
		}
	}
	if c.Database.MaxConnections < 1 || c.Database.MaxConnections > 100 {
		return fmt.Errorf("database.max_connections %d outside [1, 100]", c.Database.MaxConnections)
	}
	if c.Database.ConnectTimeoutSeconds < 1 || c.Database.ConnectTimeoutSeconds > 120 {
		return fmt.Errorf("database.connect_timeout_seconds %d outside [1, 120]", c.Database.ConnectTimeoutSeconds)
	}
	return nil
}

func (c *Config) validateRetention(r RetentionConfig, field string) error {
	if r.Payload.Duration <= 0 || r.Result.Duration <= 0 || r.Metadata.Duration <= 0 {
		return fmt.Errorf("%s durations must be positive", field)
	}
	// Ordering invariant: request payload expires first, operational
	// metadata last.
	if r.Payload.Duration > r.Result.Duration {
		return fmt.Errorf("%s.payload (%s) must not exceed %s.result (%s)", field, r.Payload.Duration, field, r.Result.Duration)
	}
	if r.Result.Duration > r.Metadata.Duration {
		return fmt.Errorf("%s.result (%s) must not exceed %s.metadata (%s)", field, r.Result.Duration, field, r.Metadata.Duration)
	}
	return nil
}

func (c *Config) validateBackends() error {
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one backend must be declared")
	}
	for name, b := range c.Backends {
		if err := domain.ValidateSlug("backend", name); err != nil {
			return fmt.Errorf("backends: %w", err)
		}
		switch b.Type {
		case AdapterFake:
			if err := validateFakeBackend(name, b); err != nil {
				return err
			}
		case AdapterOpenAICompatible:
			if err := validateOpenAICompatibleBackend(name, b); err != nil {
				return err
			}
			if b.CredentialEnv == c.Database.URLEnv ||
				b.CredentialEnv == c.Database.ProvisionerURLEnv ||
				b.CredentialEnv == c.Database.MigrationURLEnv {
				return fmt.Errorf("backends.%s.credential_env must not reference a database role URL environment variable", name)
			}
		default:
			return fmt.Errorf("backends.%s.type %q is not a supported adapter type (supported: fake, openai_compatible)", name, b.Type)
		}
		features, err := policy.CanonicalSet(fmt.Sprintf("backends.%s.features", name), b.Features)
		if err != nil {
			return err
		}
		for _, f := range features {
			if !knownFeatures[f] {
				return fmt.Errorf("backends.%s.features: unknown feature %q", name, f)
			}
		}
		b.Features = features
		c.Backends[name] = b
	}
	return nil
}

func validateFakeBackend(name string, b BackendConfig) error {
	if b.BaseURL != "" || b.Model != "" || b.CredentialEnv != "" {
		return fmt.Errorf("backends.%s: type fake must not declare base_url, model, or credential_env", name)
	}
	if b.FailMode != "" {
		switch b.FailMode {
		case FakeFailNone, FakeFailReject, FakeFailMalformed, FakeFailUnsent, FakeFailTransient, FakeFailIndeterminate:
		default:
			return fmt.Errorf("backends.%s.fail_mode %q is not supported (supported: none, reject, malformed, unsent, transient, indeterminate)", name, b.FailMode)
		}
	}
	if b.ResponseHeaderTimeoutSeconds != 0 {
		return fmt.Errorf("backends.%s: type fake must not declare response_header_timeout_seconds", name)
	}
	if b.Delay.Duration < 0 {
		return fmt.Errorf("backends.%s.delay must not be negative", name)
	}
	return nil
}

func validateOpenAICompatibleBackend(name string, b BackendConfig) error {
	if b.BaseURL == "" {
		return fmt.Errorf("backends.%s.base_url is required for type openai_compatible", name)
	}
	if err := validateBackendURL(b.BaseURL); err != nil {
		return fmt.Errorf("backends.%s.base_url: %w", name, err)
	}
	if b.Model == "" {
		return fmt.Errorf("backends.%s.model is required for type openai_compatible", name)
	}
	if b.CredentialEnv == "" {
		return fmt.Errorf("backends.%s.credential_env is required for type openai_compatible (must name the environment variable holding the credential; the value never appears in configuration)", name)
	}
	if !envNameRe.MatchString(b.CredentialEnv) {
		return fmt.Errorf("backends.%s.credential_env %q is not a valid environment variable name", name, b.CredentialEnv)
	}
	if b.Delay.Duration != 0 {
		return fmt.Errorf("backends.%s: type openai_compatible must not declare delay", name)
	}
	if b.FailMode != "" {
		return fmt.Errorf("backends.%s: type openai_compatible must not declare fail_mode", name)
	}
	if b.ResponseHeaderTimeoutSeconds != 0 && (b.ResponseHeaderTimeoutSeconds < 1 || b.ResponseHeaderTimeoutSeconds > 300) {
		return fmt.Errorf("backends.%s.response_header_timeout_seconds %d outside [1, 300]", name, b.ResponseHeaderTimeoutSeconds)
	}
	return nil
}

// validateBackendURL rejects user info, fragments, query strings,
// non-http(s) schemes, and ambiguous forms.
func validateBackendURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid backend URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q (only http and https)", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("URL must not contain user info (credentials belong in credential_env, never in the URL)")
	}
	if u.Fragment != "" {
		return fmt.Errorf("URL must not contain a fragment")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("URL must not contain a query string")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	if strings.Contains(u.Host, "/") {
		return fmt.Errorf("URL host is ambiguous")
	}
	return nil
}

func (c *Config) validateCapabilities() error {
	if len(c.Capabilities) == 0 {
		return fmt.Errorf("at least one capability must be declared")
	}
	for name, cap := range c.Capabilities {
		if err := domain.ValidateSlug("capability", name); err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
		backend, ok := c.Backends[cap.Backend]
		if !ok {
			return fmt.Errorf("capabilities.%s.backend %q does not match any declared backend", name, cap.Backend)
		}
		_ = backend
	}
	return nil
}

func (c *Config) validateWorkloads() error {
	if len(c.Workloads) == 0 {
		return fmt.Errorf("at least one workload policy must be declared")
	}
	for name, w := range c.Workloads {
		if err := domain.ValidateSlug("workload", name); err != nil {
			return fmt.Errorf("workloads: %w", err)
		}
		if w.AllowedCapabilities == nil && !w.allowedCapabilitiesSet {
			return fmt.Errorf("workloads.%s.allowed_capabilities is required (an empty array means deny-all)", name)
		}
		allowed, err := policy.CanonicalSet(fmt.Sprintf("workloads.%s.allowed_capabilities", name), w.AllowedCapabilities)
		if err != nil {
			return err
		}
		for _, capability := range allowed {
			if _, ok := c.Capabilities[capability]; !ok {
				return fmt.Errorf("workloads.%s.allowed_capabilities references unknown capability %q", name, capability)
			}
		}
		w.AllowedCapabilities = allowed
		if w.WorkloadTimeoutSeconds < 1 || w.WorkloadTimeoutSeconds > 3600 {
			return fmt.Errorf("workloads.%s.workload_timeout_seconds %d outside [1, 3600]", name, w.WorkloadTimeoutSeconds)
		}
		if _, err := domain.ParsePriorityClass(w.PriorityClass); err != nil {
			return fmt.Errorf("workloads.%s: %w", name, err)
		}
		if w.MaxActiveTasks < 1 || w.MaxActiveTasks > 1000 {
			return fmt.Errorf("workloads.%s.max_active_tasks %d outside [1, 1000]", name, w.MaxActiveTasks)
		}
		if w.MaxAttempts < 1 || w.MaxAttempts > 5 {
			return fmt.Errorf("workloads.%s.max_attempts %d outside [1, 5]", name, w.MaxAttempts)
		}
		if len(w.RetryDelaysSeconds) > w.MaxAttempts-1 {
			return fmt.Errorf("workloads.%s.retry_delays_seconds has %d entries, max %d for %d attempts", name, len(w.RetryDelaysSeconds), w.MaxAttempts-1, w.MaxAttempts)
		}
		for i, d := range w.RetryDelaysSeconds {
			if d < 1 {
				return fmt.Errorf("workloads.%s.retry_delays_seconds[%d] must be positive", name, i)
			}
		}
		if w.MaxOutputTokens < 1 || w.MaxOutputTokens > domain.MaxOutputTokens {
			return fmt.Errorf("workloads.%s.max_output_tokens %d outside [1, %d]", name, w.MaxOutputTokens, domain.MaxOutputTokens)
		}
		for field, cons := range w.AllowedGeneration {
			if !domain.IsGenerationField(field) {
				return fmt.Errorf("workloads.%s.allowed_generation: unknown field %q (supported: %s)", name, field, strings.Join(domain.GenerationFieldNames, ", "))
			}
			if err := validateGenerationConstraint(field, cons); err != nil {
				return fmt.Errorf("workloads.%s.allowed_generation.%s: %w", name, field, err)
			}
		}
		features, err := policy.CanonicalSet(fmt.Sprintf("workloads.%s.required_features", name), w.RequiredFeatures)
		if err != nil {
			return err
		}
		for _, f := range features {
			if !knownFeatures[f] {
				return fmt.Errorf("workloads.%s.required_features: unknown feature %q", name, f)
			}
		}
		w.RequiredFeatures = features
		if w.Retention != nil {
			if err := c.validateRetention(*w.Retention, fmt.Sprintf("workloads.%s.retention", name)); err != nil {
				return err
			}
		}
		c.Workloads[name] = w
	}
	return nil
}

func validateGenerationConstraint(field string, cons GenerationConstraint) error {
	if cons.Min != nil && cons.Max != nil && *cons.Min > *cons.Max {
		return fmt.Errorf("min %v exceeds max %v", *cons.Min, *cons.Max)
	}
	if cons.MaxItems != nil && *cons.MaxItems < 1 {
		return fmt.Errorf("max_items must be positive")
	}
	// Narrow the documented intrinsic bounds: constraints must lie within
	// the intrinsic range of the field so policies can only restrict.
	if field == "max_tokens" || field == "top_k" {
		var lo, hi float64
		if field == "max_tokens" {
			lo, hi = float64(domain.MaxTokensMin), float64(domain.MaxTokensMax)
		} else {
			lo, hi = float64(domain.TopKMin), float64(domain.TopKMax)
		}
		if cons.Min != nil && *cons.Min < lo {
			return fmt.Errorf("min %v below intrinsic bound %v", *cons.Min, lo)
		}
		if cons.Max != nil && *cons.Max > hi {
			return fmt.Errorf("max %v above intrinsic bound %v", *cons.Max, hi)
		}
	}
	return nil
}
