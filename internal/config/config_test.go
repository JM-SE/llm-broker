package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
)

func validConfigJSON() string {
	return `{
		"version": 1,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
		"backends": {
			"fake": {"type": "fake"}
		},
		"capabilities": {"instruct": {"backend": "fake"}},
		"workloads": {
			"default": {"priority_class": "normal"}
		}
	}`
}

func TestDecodeValid(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfigJSON()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Version != 1 {
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

func TestDecodeRejectsUnknownFields(t *testing.T) {
	cases := []struct{ name, json, field, value string }{
		{"top-level", `{"version": 1, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal"}}, "secret": "s3cr3t-top"}`,
			"secret", "s3cr3t-top"},
		{"api", `{"version": 1, "api": {"listen": "127.0.0.1:8010", "tls": true}, "database": {"url_env": "X"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal"}}}`,
			"tls", ""},
		{"backend", `{"version": 1, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake", "api_key": "s3cr3t-back"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal"}}}`,
			"api_key", "s3cr3t-back"},
		{"workload", `{"version": 1, "api": {"listen": "127.0.0.1:8010"}, "database": {"url_env": "X"}, "retention": {"payload": "1h", "result": "2h", "metadata": "3h"}, "backends": {"fake": {"type": "fake"}}, "capabilities": {"c": {"backend": "fake"}}, "workloads": {"w": {"priority_class": "normal", "model": "x"}}}`,
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
	json := strings.Replace(validConfigJSON(), `"version": 1`, `"version": 2`, 1)
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
	json := `{"version": 1, "version": 2}`
	if _, err := Decode(strings.NewReader(json)); err == nil {
		t.Error("duplicate fields should fail")
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

func TestCapabilityMustResolve(t *testing.T) {
	json := `{
		"version": 1,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL"},
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
			"database":  map[string]interface{}{"url_env": "LLM_BROKER_DATABASE_URL"},
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
		"database": {"url_env": "LLM_BROKER_DATABASE_URL"},
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
