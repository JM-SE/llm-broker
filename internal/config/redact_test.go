package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactedJSONMasksCredentials(t *testing.T) {
	json := `{
		"version": 2,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {
			"primary": {"type": "openai_compatible", "base_url": "http://llm-runtime.example:8080/v1", "model": "alias", "credential_env": "LLM_BACKEND_API_KEY"}
		},
		"capabilities": {"instruct": {"backend": "primary"}},
		"workloads": {"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}}
	}`
	cfg, err := Decode(strings.NewReader(json))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "LLM_BACKEND_API_KEY") {
		t.Errorf("redacted output leaked credential env name: %s", s)
	}
	if !strings.Contains(s, `"credential_env": "***"`) {
		t.Errorf("redacted output should mask credential_env: %s", s)
	}
	if strings.Contains(s, "supersecret") {
		t.Error("unexpected secret material")
	}
	// Round-trip: redacted output must still be valid JSON.
	if !bytes.HasPrefix(bytes.TrimSpace(out), []byte("{")) {
		t.Errorf("redacted output is not an object: %q", out)
	}
}

func TestRedactedJSONNoZeroDelay(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfigJSON()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"delay"`) {
		t.Errorf("zero delay should be omitted: %s", out)
	}
}

func TestConfigNeverReadsEnvSecrets(t *testing.T) {
	t.Setenv("LLM_BACKEND_API_KEY", "super-secret-value-42")
	json := `{
		"version": 2,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {
			"primary": {"type": "openai_compatible", "base_url": "http://llm-runtime.example:8080/v1", "model": "alias", "credential_env": "LLM_BACKEND_API_KEY"}
		},
		"capabilities": {"instruct": {"backend": "primary"}},
		"workloads": {"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}}
	}`
	cfg, err := Decode(strings.NewReader(json))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.RedactedJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "super-secret-value-42") {
		t.Error("redacted output leaked a loaded secret value")
	}
}

func TestValidationErrorsIdentifyField(t *testing.T) {
	json := `{
		"version": 2,
		"api": {"listen": "not-a-port"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_DATABASE_URL", "migration_url_env": "LLM_BROKER_MIGRATION_DATABASE_URL"},
		"retention": {"payload": "1h", "result": "2h", "metadata": "3h"},
		"backends": {"fake": {"type": "fake"}},
		"capabilities": {"instruct": {"backend": "fake"}},
		"workloads": {"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}}
	}`
	_, err := Decode(strings.NewReader(json))
	if err == nil {
		t.Fatal("invalid listen must fail")
	}
	if !strings.Contains(err.Error(), "api.listen") {
		t.Errorf("error should identify the field: %v", err)
	}
}
