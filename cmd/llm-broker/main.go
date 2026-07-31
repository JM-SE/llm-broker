package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/JM-SE/llm-broker/internal/version"
)

const (
	exitOk             = 0
	exitUsage          = 1
	exitNotImplemented = 1
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), signals()...)
	defer cancel()
	os.Exit(run(ctx, os.Args, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		printUsageTo(stderr)
		return exitUsage
	}

	cmd := args[1]
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version.String())
		return exitOk
	case "help", "--help", "-h":
		printUsageTo(stderr)
		return exitOk
	case "api":
		fmt.Fprintln(stderr, "llm-broker api: not implemented")
		return exitNotImplemented
	case "worker":
		fmt.Fprintln(stderr, "llm-broker worker: not implemented")
		return exitNotImplemented
	case "migrate":
		return runMigrate(ctx, args[2:], stdout, stderr)
	case "client":
		fmt.Fprintln(stderr, "llm-broker client: not implemented")
		return exitNotImplemented
	case "config":
		return runConfig(args[2:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", cmd)
		printUsageTo(stderr)
		return exitUsage
	}
}

func printUsage() {
	printUsageTo(os.Stderr)
}

func printUsageTo(w io.Writer) {
	fmt.Fprintf(w, `llm-broker — durable single-host inference scheduler

usage:
  llm-broker <command> [arguments]

commands:
  api       start the HTTP API server
  worker    start the scheduling and inference worker
  migrate   manage database migrations
  client    manage API credentials and workload grants
  config    validate or inspect configuration
  version   print version and build information

config:
  llm-broker config validate [path]
  llm-broker config show [path]
    path defaults to $LLM_BROKER_CONFIG_FILE

migrate:
  llm-broker migrate status [path]
  llm-broker migrate up [path]
    applies embedded SQL migrations; the database URL is read from the
    environment variable configured as database.url_env in the config

`)
}
