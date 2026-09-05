package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatDesiredPrintsSafeCompatibleDecision(t *testing.T) {
	manifestPath, configPath := writeManifestAndConfig(t)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "compat", "desired", "--manifest", manifestPath, configPath}, &stdout, &stderr)
	if code != exitOk {
		t.Fatalf("compat desired exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[0] != "compatible" || !strings.HasPrefix(lines[1], "sha256:") {
		t.Fatalf("unsafe or unstable output: %q", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "DATABASE_URL") {
		t.Fatal("credential environment detail leaked")
	}
}

func TestCompatArgumentOrderAndUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"llm-broker", "compat", "desired", "--manifest"},
		{"llm-broker", "compat", "desired", "--manifest", "manifest", "config", "other"},
		{"llm-broker", "compat", "desired", "--manifest", "manifest", "--unknown"},
		{"llm-broker", "compat", "effective", "--manifest", "manifest"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != exitUsage {
			t.Errorf("args %v exit=%d, want usage", args, code)
		}
	}
}

func TestCompatHelpIsRegistered(t *testing.T) {
	var buf bytes.Buffer
	printUsageTo(&buf)
	if !strings.Contains(buf.String(), "compat") {
		t.Fatal("top-level help does not mention compat")
	}
	buf.Reset()
	if code := run(context.Background(), []string{"llm-broker", "compat", "help"}, &buf, &buf); code != exitOk || !strings.Contains(buf.String(), "--manifest") {
		t.Fatalf("compat help = %d %q", code, buf.String())
	}
}

// writeManifestAndConfig stages one generic compatible manifest alongside a
// fake-backend configuration that satisfies it. Tests must never embed
// provider URLs, model names, credentials, or database details in either
// document.
func writeManifestAndConfig(t *testing.T) (manifestPath, configPath string) {
	t.Helper()
	dir := t.TempDir()
	manifestPath = filepath.Join(dir, "manifest.json")
	configPath = filepath.Join(dir, "config.json")
	if err := os.WriteFile(manifestPath, []byte(`{
  "version":"consumer-compat/v1","api_range":">=0.1.1 <0.2.0",
  "workload":"generic-workload","capability":"generic-capability",
  "generation":{"max_tokens":128},"response":{"kind":"text"},
  "max_request_content_bytes":1,"required_min_output_bytes":1,
  "min_workload_timeout_seconds":0,"min_backend_response_start_timeout_seconds":0,
  "max_acceptable_attempts":1,"consumer_overall_deadline_seconds":2
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version":2,"api":{"listen":"127.0.0.1:8010"},
  "database":{"url_env":"DATABASE_URL","provisioner_url_env":"PROVISIONER_URL","migration_url_env":"MIGRATION_URL"},
  "retention":{"payload":"1h","result":"2h","metadata":"3h"},
  "backends":{"backend":{"type":"fake"}},
  "capabilities":{"generic-capability":{"backend":"backend"}},
  "workloads":{"generic-workload":{"priority_class":"normal","max_attempts":1,"max_output_bytes":1,"allowed_generation":{"max_tokens":{"max":128}},"allowed_capabilities":["generic-capability"],"workload_timeout_seconds":1}}
}`), 0600); err != nil {
		t.Fatal(err)
	}
	return manifestPath, configPath
}

// TestCompatDesiredReportsIncompatibleWithExitTwo freezes the F4 contract:
// a valid manifest whose requirements exceed the configured workload exits
// with exitIncompatible (2) and prints a deterministic ordered finding set.
func TestCompatDesiredReportsIncompatibleWithExitTwo(t *testing.T) {
	manifestPath, configPath := writeManifestAndConfig(t)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// Demand more output bytes than the configured workload allows (1 byte).
	over, err := os.Create(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw),
		`"required_min_output_bytes":1`, `"required_min_output_bytes":4096`, 1)
	if _, err := over.WriteString(patched); err != nil {
		t.Fatal(err)
	}
	if err := over.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "compat", "desired", "--manifest", manifestPath, configPath}, &stdout, &stderr)
	if code != exitIncompatible {
		t.Fatalf("compat desired exit=%d, want %d stderr=%q stdout=%q", code, exitIncompatible, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || lines[0] != "incompatible" || !strings.HasPrefix(lines[1], "sha256:") {
		t.Fatalf("unstable incompatible output: %q", stdout.String())
	}
	if lines[2] != "output_bytes_limit_insufficient=1" {
		t.Fatalf("incompatible findings = %q, want output_bytes_limit_insufficient=1", lines[2])
	}
	if strings.Contains(stdout.String()+stderr.String(), "DATABASE_URL") {
		t.Fatal("credential environment detail leaked")
	}
}

// TestCompatOutputsDiscloseNoSecrets sweeps every compat output path with
// secret-marked material: distinct sentinels for the backend URL, model,
// credential environment name, database URLs, and manifest content must never
// appear on stdout or stderr, on success, drift, parse failure, or
// operational failure paths.
func TestCompatOutputsDiscloseNoSecrets(t *testing.T) {
	dir := t.TempDir()
	manifestPath := dir + "/manifest.json"
	configPath := dir + "/config.json"
	if err := os.WriteFile(manifestPath, []byte(`{
  "version":"consumer-compat/v1","api_range":">=0.1.1 <0.2.0",
  "workload":"generic-workload","capability":"generic-capability",
  "generation":{"max_tokens":128},"response":{"kind":"text"},
  "max_request_content_bytes":1,"required_min_output_bytes":4096,
  "min_workload_timeout_seconds":0,"min_backend_response_start_timeout_seconds":0,
  "max_acceptable_attempts":1,"consumer_overall_deadline_seconds":2
}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version":2,"api":{"listen":"127.0.0.1:8010"},
  "database":{"url_env":"SENTINEL_RUNTIME_DB_URL","provisioner_url_env":"SENTINEL_PROVISIONER_DB_URL","migration_url_env":"SENTINEL_MIGRATION_DB_URL"},
  "retention":{"payload":"1h","result":"2h","metadata":"3h"},
  "backends":{"sentinel-backend":{"type":"openai_compatible","base_url":"https://sentinel-backend-url.invalid/v1","model":"sentinel-model-alias","credential_env":"SENTINEL_BACKEND_CREDENTIAL"}},
  "capabilities":{"generic-capability":{"backend":"sentinel-backend"}},
  "workloads":{"generic-workload":{"priority_class":"normal","max_attempts":1,"max_output_bytes":1,"allowed_generation":{"max_tokens":{"max":128}},"allowed_capabilities":["generic-capability"],"workload_timeout_seconds":1}}
}`), 0600); err != nil {
		t.Fatal(err)
	}
	sentinels := []string{
		"sentinel-backend-url", "sentinel-model-alias", "SENTINEL_BACKEND_CREDENTIAL",
		"SENTINEL_RUNTIME_DB_URL", "SENTINEL_PROVISIONER_DB_URL", "SENTINEL_MIGRATION_DB_URL",
		"sentinel-backend",
	}
	check := func(name string, code int, stdout, stderr bytes.Buffer) {
		t.Helper()
		combined := stdout.String() + stderr.String()
		for _, sentinel := range sentinels {
			if strings.Contains(combined, sentinel) {
				t.Fatalf("%s exit=%d disclosed %q in %q", name, code, sentinel, combined)
			}
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "compat", "desired", "--manifest", manifestPath, configPath}, &stdout, &stderr)
	if code != exitIncompatible {
		t.Fatalf("drift exit=%d, want %d", code, exitIncompatible)
	}
	check("drift", code, stdout, stderr)

	// Parse failure path: manifest content itself carries a sentinel.
	stdout.Reset()
	stderr.Reset()
	badManifest := dir + "/bad.json"
	if err := os.WriteFile(badManifest, []byte(`{"version":"consumer-compat/v1","sentinel-manifest-marker":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	code = run(context.Background(), []string{"llm-broker", "compat", "desired", "--manifest", badManifest, configPath}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("parse-failure exit=%d, want %d", code, exitUsage)
	}
	if strings.Contains(stdout.String()+stderr.String(), "sentinel-manifest-marker") {
		t.Fatalf("parse failure echoed manifest content: %q", stdout.String()+stderr.String())
	}
	check("parse-failure", code, stdout, stderr)

	// Operational failure path: effective mode without a provisioner URL.
	stdout.Reset()
	stderr.Reset()
	t.Setenv("SENTINEL_PROVISIONER_DB_URL", "")
	code = run(context.Background(), []string{"llm-broker", "compat", "effective", "--manifest", manifestPath, "--client", "sentinel-client", configPath}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("operational-failure exit=%d, want %d", code, exitUsage)
	}
	check("operational-failure", code, stdout, stderr)
}
