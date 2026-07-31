package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "version"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("version exit code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "llm-broker") {
		t.Errorf("version output missing program name: %s", output)
	}
}

func TestHelp(t *testing.T) {
	for _, cmd := range []string{"help", "--help", "-h"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"llm-broker", cmd}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("%s exit code = %d, want 0", cmd, code)
		}
		if !strings.Contains(stderr.String(), "llm-broker") {
			t.Errorf("%s usage missing program name", cmd)
		}
	}
}

func TestNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("no-args exit code = %d, want 1", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"llm-broker", "nonexistent"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("unknown exit code = %d, want 1", code)
	}
	output := stderr.String()
	if !strings.Contains(output, "unknown command") {
		t.Errorf("stderr missing unknown command: %s", output)
	}
}

func TestPrintUsageTo(t *testing.T) {
	var buf bytes.Buffer
	printUsageTo(&buf)
	output := buf.String()
	for _, want := range []string{"llm-broker", "api", "worker", "migrate", "client", "config", "version"} {
		if !strings.Contains(output, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}
