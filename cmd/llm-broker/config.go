package main

import (
	"fmt"
	"io"
	"os"

	"github.com/JM-SE/llm-broker/internal/config"
)

const configEnvVar = "LLM_BROKER_CONFIG_FILE"

// runConfig implements the `config` subcommand: validate and show.
// Neither subcommand reads secret environment variables, performs network
// access, or touches persistence.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "llm-broker config: missing subcommand (validate|show)")
		return exitUsage
	}
	switch args[0] {
	case "validate":
		return runConfigValidate(args[1:], stdout, stderr)
	case "show":
		return runConfigShow(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, "usage: llm-broker config validate|show [path]")
		return exitOk
	default:
		fmt.Fprintf(stderr, "llm-broker config: unknown subcommand %q (validate|show)\n", args[0])
		return exitUsage
	}
}

func resolveConfigPath(args []string, stderr io.Writer) (string, bool) {
	if len(args) > 0 && args[0] != "" {
		return args[0], true
	}
	if p := os.Getenv(configEnvVar); p != "" {
		return p, true
	}
	fmt.Fprintf(stderr, "llm-broker config: no configuration path; pass one as an argument or set %s\n", configEnvVar)
	return "", false
}

func runConfigValidate(args []string, stdout, stderr io.Writer) int {
	path, ok := resolveConfigPath(args, stderr)
	if !ok {
		return exitUsage
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	_ = cfg
	fmt.Fprintf(stdout, "ok: %s is a valid configuration (version %d)\n", config.FilePath(path), config.Version)
	return exitOk
}

func runConfigShow(args []string, stdout, stderr io.Writer) int {
	path, ok := resolveConfigPath(args, stderr)
	if !ok {
		return exitUsage
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	out, err := cfg.RedactedJSON()
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker config show: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintf(stderr, "llm-broker config show: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\n# source: %s\n", config.FilePath(path))
	return exitOk
}
