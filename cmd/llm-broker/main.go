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
		return runAPI(ctx, args[2:], stdout, stderr)
	case "worker":
		return runWorker(ctx, args[2:], stdout, stderr)
	case "migrate":
		return runMigrate(ctx, args[2:], stdout, stderr)
	case "client":
		return runClient(ctx, args[2:], stdout, stderr)
	case "config":
		return runConfig(args[2:], stdout, stderr)
	case "policy":
		return runPolicy(ctx, args[2:], stdout, stderr)
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
  policy    validate, diff, apply, or inspect durable workload policy
  version   print version and build information

config:
  llm-broker config validate [path]
  llm-broker config show [path]
    path defaults to $LLM_BROKER_CONFIG_FILE

migrate:
  llm-broker migrate status [path]
  llm-broker migrate up [path]
    applies embedded SQL migrations; the database URL is read from the
    environment variable configured as database.migration_url_env in the config

api:
  llm-broker api
    starts the HTTP API server; configuration comes from $LLM_BROKER_CONFIG_FILE

worker:
  llm-broker worker
  llm-broker worker status
  llm-broker worker resolve <task-id> --outcome=requeue|failed [--delay=N]
    runs the execution worker or inspects/resolves ambiguous executions;
    configuration comes from $LLM_BROKER_CONFIG_FILE

client:
  llm-broker client create <name>
  llm-broker client disable <name>
  llm-broker client revoke <name>
  llm-broker client grant <name> <workload>
  llm-broker client ungrant <name> <workload>
    create/revoke require --credential-file <path>; credentials are written
    once with mode 0600 and never printed

policy:
  llm-broker policy validate [path]
  llm-broker policy diff|apply|status [path]
    validate is database-free; the other commands require the provisioner role

`)
}
