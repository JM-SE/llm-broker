package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
)

func validConfigJSON() string {
	return `{
		"version": 2,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
		"backends": {
			"fake": {"type": "fake"}
		},
		"capabilities": {"instruct": {"backend": "fake"}},
		"workloads": {
			"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}
		}
	}`
}

func TestDecodeValid(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfigJSON()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Version != 2 {
		t.Errorf("version = %d", cfg.Version)
	}
	// Defaults applied.
	if cfg.API.Listen != "127.0.0.1:8010" {
		t.Errorf("listen = %q", cfg.API.Listen)
	}
	if cfg.API.BodyLimitBytes != DefaultBodyLimitBytes {
		t.Errorf("body limit = %d", cfg.API.BodyLimitBytes)
	}
	if cfg.Workloads["default"].MaxAttempts != domain.DefaultMaxAttempts {
		t.Errorf("max attempts = %d", cfg.Workloads["default"].MaxAttempts)
	}
	if cfg.Workloads["default"].PriorityClass != "normal" {
		t.Errorf("priority = %q", cfg.Workloads["default"].PriorityClass)
	}
	if cfg.Retention.Payload.Duration != time.Hour {
		t.Errorf("payload retention = %v", cfg.Retention.Payload.Duration)
	}
}

func TestYTPipeExampleMatchesProbeContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(source), "..", "..", "examples", "ytpipe-openai-compatible.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read YTPipe example: %v", err)
	}
	cfg, err := Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("decode YTPipe example: %v", err)
	}
	workload, ok := cfg.Workloads["batch-summary"]
	if !ok {
		t.Fatal("YTPipe workload is missing")
	}
	if workload.MaxOutputTokens != 512 || len(workload.AllowedCapabilities) != 1 || workload.AllowedCapabilities[0] != "summarize" {
		t.Fatalf("unexpected YTPipe workload: %+v", workload)
	}
	projected, err := cfg.PolicyProjections()
	if err != nil {
		t.Fatalf("project YTPipe policy: %v", err)
	}
	var batch policy.Workload
	for _, candidate := range projected {
		if candidate.Name == "batch-summary" {
			batch = candidate
			break
		}
	}
	maxTokens := int64(512)
	temperature := 0.7
	if err := policy.ValidateGeneration(domain.Generation{
		MaxTokens: &maxTokens, Temperature: &temperature,
	}, batch.AllowedGeneration, batch.MaxOutputTokens); err != nil {
		t.Fatalf("YTPipe probe generation rejected: %v", err)
	}
}

func TestDatabaseRoleEnvironmentReferencesAreDistinctAndRequired(t *testing.T) {
	cases := []string{
		`{"url_env":"LLM_BROKER_DATABASE_URL","provisioner_url_env":"LLM_BROKER_DATABASE_URL","migration_url_env":"LLM_BROKER_MIGRATION_DATABASE_URL"}`,
		`{"url_env":"LLM_BROKER_DATABASE_URL","provisioner_url_env":"LLM_BROKER_PROVISIONER_DATABASE_URL"}`,
	}
	for _, database := range cases {
		doc := strings.Replace(validConfigJSON(), `{"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"}`, database, 1)
		if _, err := Decode(strings.NewReader(doc)); err == nil {
			t.Errorf("database role references %s should fail", database)
		}
	}
}

func TestBackendCredentialEnvironmentCannotBeDatabaseURLReference(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfigJSON()))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backends = map[string]BackendConfig{
		"primary": {
			Type:          AdapterOpenAICompatible,
			BaseURL:       "http://runtime.example/v1",
			Model:         "operator-model",
			CredentialEnv: cfg.Database.URLEnv,
		},
	}
	cfg.Capabilities = map[string]Capability{"instruct": {Backend: "primary"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "credential_env") {
		t.Fatalf("database URL credential collision should fail without secret detail: %v", err)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	cases := []struct{ name, json, field, value string }{
		{"top-level", `{"version": 2, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X", "provisioner_url_env": "Y", "migration_url_env": "Z"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal", "allowed_capabilities": ["c"], "workload_timeout_seconds": 300}}, "secret": "s3cr3t-top"}`,
			"secret", "s3cr3t-top"},
		{"api", `{"version": 2, "api": {"listen": "127.0.0.1:8010", "tls": true}, "database": {"url_env": "X", "provisioner_url_env": "Y", "migration_url_env": "Z"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal", "allowed_capabilities": ["c"], "workload_timeout_seconds": 300}}}`,
			"tls", ""},
		{"backend", `{"version": 2, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X", "provisioner_url_env": "Y", "migration_url_env": "Z"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake", "api_key": "s3cr3t-back"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal", "allowed_capabilities": ["c"], "workload_timeout_seconds": 300}}}`,
			"api_key", "s3cr3t-back"},
		{"workload", `{"version": 2, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X", "provisioner_url_env": "Y", "migration_url_env": "Z"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal", "allowed_capabilities": ["c"], "workload_timeout_seconds": 300, "model": "x"}}}`,
			"model", ""},
	}
	for _, c := range cases {
		_, decErr := Decode(strings.NewReader(c.json))
		if decErr == nil {
			t.Errorf("%s: unknown field should fail", c.name)
			continue
		}
		msg := decErr.Error()
		if !strings.Contains(msg, c.field) {
			t.Errorf("%s: error should identify field %q: %s", c.name, c.field, msg)
		}
		if c.value != "" && strings.Contains(msg, c.value) {
			t.Errorf("%s: error echoed a sensitive value: %s", c.name, msg)
		}
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	json := strings.Replace(validConfigJSON(), `"version": 2`, `"version": 1`, 1)
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("version 2 should fail")
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	if _, err := Decode(strings.NewReader(validConfigJSON() + ` {"extra": true}`)); err == nil {
		t.Error("trailing content should fail")
	}
}

func TestDecodeRejectsDuplicateFields(t *testing.T) {
	json := `{"version": 2, "version": 1}`
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("duplicate fields should fail")
	}
}

func TestWorkloadV2RequiredFieldsAndTimeout(t *testing.T) {
	for _, replacement := range []string{
		`"allowed_capabilities": ["instruct"]`,
		`"workload_timeout_seconds": 300`,
		`"workload_timeout_seconds": null`,
		`"workload_timeout_seconds": 0`,
		`"workload_timeout_seconds": 3601`,
	} {
		json := strings.Replace(validConfigJSON(), `"allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300`, replacement, 1)
		if _, err := Decode(strings.NewReader(json)); err == nil {
			t.Errorf("replacement %s should fail", replacement)
		}
	}
	for _, timeout := range []string{"1", "3600"} {
		json := strings.Replace(validConfigJSON(), `"workload_timeout_seconds": 300`, `"workload_timeout_seconds": `+timeout, 1)
		if _, err := Decode(strings.NewReader(json)); err != nil {
			t.Errorf("timeout %s should pass: %v", timeout, err)
		}
	}
}

func TestAllowedCapabilitiesEmptyMeansDenyAll(t *testing.T) {
	json := strings.Replace(validConfigJSON(), `"allowed_capabilities": ["instruct"]`, `"allowed_capabilities": []`, 1)
	cfg, err := Decode(strings.NewReader(json))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Workloads["default"].AllowedCapabilities) != 0 {
		t.Fatal("empty capability allowlist was not preserved")
	}
}

func TestBackendURLRejection(t *testing.T) {
	bad := []string{
		"ftp://host/v1",
		"https://user:pass@host/v1",
		"https://host/v1#frag",
		"https://host/v1?q=1",
		"host:8001/v1",
		"",
		"//host/v1",
		"file:///etc/passwd",
	}
	for _, u := range bad {
		if err := validateBackendURL(u); err == nil {
			t.Errorf("URL should be rejected: %q", u)
		}
	}
	good := []string{"http://127.0.0.1:8080/v1", "https://llm-runtime.example:8080/v1", "http://localhost:8080"}
	for _, u := range good {
		if err := validateBackendURL(u); err != nil {
			t.Errorf("URL should be accepted: %q: %v", u, err)
		}
	}
}

func TestBackendURLParseErrorsDoNotEchoSecrets(t *testing.T) {
	const secret = "backend-secret-value"
	if err := validateBackendURL("http://%zz:" + secret + "@host/v1"); err == nil {
		t.Fatal("malformed URL unexpectedly accepted")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("URL validation echoed credential material: %v", err)
	}
}

func TestCapabilityMustResolve(t *testing.T) {
	json := `{
		"version": 1,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {"fake": {"type": "fake"}},
		"capabilities": {"instruct": {"backend": "missing"}},
		"workloads": {"default": {"priority_class": "normal"}}
	}`
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("capability referencing missing backend must fail")
	}
}

func TestWorkloadPolicyValidation(t *testing.T) {
	base := func(mutate func(m map[string]interface{})) string {
		m := map[string]interface{}{
			"version":   1,
			"api":       map[string]interface{}{"listen": "127.0.0.1:8010"},
			"database":  map[string]interface{}{"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
			"retention": map[string]interface{}{"payload": "1h", "result": "2h", "metadata": "3h"},
			"backends":  map[string]interface{}{"fake": map[string]interface{}{"type": "fake"}},
			"capabilities": map[string]interface{}{
				"instruct": map[string]interface{}{"backend": "fake"}},
			"workloads": map[string]interface{}{
				"default": map[string]interface{}{"priority_class": "normal"}},
		}
		mutate(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	cases := []struct {
		name string
		json string
	}{
		{"bad priority", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "urgent"}
		})},
		{"too many attempts", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "max_attempts": 6}
		})},
		{"too many delays", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "max_attempts": 2, "retry_delays_seconds": []int{1, 2, 3}}
		})},
		{"output over hard cap", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "max_output_tokens": domain.MaxOutputTokens + 1}
		})},
		{"unknown generation field", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "allowed_generation": map[string]interface{}{"frequency_penalty": map[string]interface{}{}}}
		})},
		{"constraint above intrinsic", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "allowed_generation": map[string]interface{}{"max_tokens": map[string]interface{}{"max": domain.MaxOutputTokens + 1}}}
		})},
		{"unknown feature", base(func(m map[string]interface{}) {
			m["workloads"].(map[string]interface{})["default"] = map[string]interface{}{"priority_class": "normal", "required_features": []string{"streaming"}}
		})},
		{"retention order", base(func(m map[string]interface{}) {
			m["retention"] = map[string]interface{}{"payload": "3h", "result": "2h", "metadata": "1h"}
		})},
	}
	for _, c := range cases {
		if _, err := Decode(strings.NewReader(c.json)); err == nil {
			t.Errorf("%s: should fail", c.name)
		}
	}
}

func TestOpenAICompatibleValidation(t *testing.T) {
	// Missing pieces must fail.
	json := `{
		"version": 1,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {"primary": {"type": "openai_compatible", "base_url": "http://llm-runtime.example:8080/v1", "model": "alias"}},
		"capabilities": {"instruct": {"backend": "primary"}},
		"workloads": {"default": {"priority_class": "normal"}}
	}`
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("openai_compatible without credential_env must fail")
	}
}

func TestDatabaseEnvNameValidation(t *testing.T) {
	json := `{
		"version": 1,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "db url"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {"fake": {"type": "fake"}},
		"capabilities": {"instruct": {"backend": "fake"}},
		"workloads": {"default": {"priority_class": "normal"}}
	}`
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("invalid env name must fail")
	}
}

func TestRetentionDefaultsAndOrdering(t *testing.T) {
	// Defaults.
	cfg, err := Decode(strings.NewReader(validConfigJSON()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.Payload.Duration != time.Hour {
		t.Errorf("payload default = %v", cfg.Retention.Payload.Duration)
	}
	// Payload > result must fail.
	json := strings.Replace(validConfigJSON(), `"payload": "1h", "result": "168h"`, `"payload": "240h", "result": "168h"`, 1)
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("payload > result must fail")
	}
}

func TestDurationJSON(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"2m"`)); err != nil {
		t.Fatal(err)
	}
	if d.Duration != 2*time.Minute {
		t.Errorf("duration = %v", d.Duration)
	}
	// Non-string must fail cleanly.
	if err := d.UnmarshalJSON([]byte(`2`)); err == nil {
		t.Error("numeric duration must fail")
	}
}
