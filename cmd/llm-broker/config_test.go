package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const testValidConfig = `{
	"version": 2,
	"api": {"listen": "127.0.0.1:8010"},
	"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_URL", "migration_url_env": "LLM_BROKER_MIGRATION_URL"},
	"retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
	"backends": {"fake": {"type": "fake"}},
	"capabilities": {"instruct": {"backend": "fake"}},
	"workloads": {"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}}
}`

func TestConfigValidateOK(t *testing.T) {
	path := writeTempConfig(t, testValidConfig)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "validate", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid") {
		t.Errorf("stdout missing ok: %s", stdout.String())
	}
}

func TestConfigValidateInvalid(t *testing.T) {
	path := writeTempConfig(t, `{"version": 2}`)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "validate", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "invalid configuration") {
		t.Errorf("stderr: %s", stderr.String())
	}
}

func TestConfigValidateMissingPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "validate"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "LLM_BROKER_CONFIG_FILE") {
		t.Errorf("stderr should mention the env var: %s", stderr.String())
	}
}

func TestConfigValidateEnvFallback(t *testing.T) {
	path := writeTempConfig(t, testValidConfig)
	t.Setenv("LLM_BROKER_CONFIG_FILE", path)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "validate"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
}

func TestConfigShowRedacted(t *testing.T) {
	path := writeTempConfig(t, `{
		"version": 2,
		"api": {"listen": "127.0.0.1:8010"},
		"database": {"url_env": "LLM_BROKER_DATABASE_URL", "provisioner_url_env": "LLM_BROKER_PROVISIONER_URL", "migration_url_env": "LLM_BROKER_MIGRATION_URL"},
		"retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
		"backends": {
			"primary": {"type": "openai_compatible", "base_url": "http://llm-runtime.example:8080/v1", "model": "alias", "credential_env": "LLM_BACKEND_API_KEY"}
		},
		"capabilities": {"instruct": {"backend": "primary"}},
		"workloads": {"default": {"priority_class": "normal", "allowed_capabilities": ["instruct"], "workload_timeout_seconds": 300}}
	}`)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "show", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "LLM_BACKEND_API_KEY") {
		t.Errorf("show leaked credential env name: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("show should mask credentials: %s", out)
	}
}

func TestConfigUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "config", "frobnicate"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
}
