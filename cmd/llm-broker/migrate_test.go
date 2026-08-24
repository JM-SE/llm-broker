package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The non-database paths of the migrate command: argument handling, config
// resolution, and environment-variable errors. Real migration behavior is
// covered by the store integration tests against a disposable database.

func TestMigrateMissingSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "migrate"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "missing subcommand") {
		t.Errorf("stderr: %s", stderr.String())
	}
}

func TestMigrateUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "migrate", "down"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Errorf("stderr: %s", stderr.String())
	}
}

func TestMigrateHelp(t *testing.T) {
	for _, sub := range []string{"help", "--help", "-h"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"llm-broker", "migrate", sub}, &stdout, &stderr)
		if code != exitOk {
			t.Errorf("%s exit = %d, want %d", sub, code, exitOk)
		}
		if !strings.Contains(stderr.String(), "status|up") {
			t.Errorf("%s stderr: %s", sub, stderr.String())
		}
	}
}

func TestMigrateMissingConfig(t *testing.T) {
	t.Setenv(configEnvVar, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "migrate", "status"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), configEnvVar) {
		t.Errorf("stderr should mention %s: %s", configEnvVar, stderr.String())
	}
}

func TestMigrateMissingDatabaseURL(t *testing.T) {
	path := writeTempConfig(t, testValidConfig)
	t.Setenv("LLM_BROKER_MIGRATION_URL", "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "migrate", "status", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "LLM_BROKER_MIGRATION_URL") {
		t.Errorf("stderr should name the URL environment variable: %s", stderr.String())
	}
}
