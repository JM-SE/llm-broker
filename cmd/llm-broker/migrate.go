package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/migrations"
	"github.com/pressly/goose/v3"
)

// runMigrate implements the `migrate` subcommand: status and up. Migrations
// are embedded in the binary and are never applied automatically at startup;
// an operator decides when to run `llm-broker migrate up` against which
// database (via the configured URL environment variable).
func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "llm-broker migrate: missing subcommand (status|up)")
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "status", "up":
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, "usage: llm-broker migrate status|up [config path]")
		return exitOk
	default:
		fmt.Fprintf(stderr, "llm-broker migrate: unknown subcommand %q (status|up)\n", sub)
		return exitUsage
	}

	path, ok := resolveConfigPath(args[1:], stderr)
	if !ok {
		return exitUsage
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	url := os.Getenv(cfg.Database.URLEnv)
	if url == "" {
		fmt.Fprintf(stderr, "llm-broker migrate: environment variable %s is empty or unset\n", cfg.Database.URLEnv)
		return 1
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker migrate: %v\n", err)
		return 1
	}
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker migrate: %v\n", err)
		return 1
	}
	defer provider.Close()

	switch sub {
	case "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "llm-broker migrate status: %v\n", err)
			return 1
		}
		for _, s := range statuses {
			applied := ""
			if s.State == goose.StateApplied {
				applied = s.AppliedAt.UTC().Format("2006-01-02 15:04:05")
			}
			fmt.Fprintf(stdout, "%-7s %-40s %s\n", s.State, filepath.Base(s.Source.Path), applied)
		}
		dbv, err := provider.GetDBVersion(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "llm-broker migrate status: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "database version: %d\n", dbv)
		return exitOk
	case "up":
		results, err := provider.Up(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "llm-broker migrate up: %v\n", err)
			return 1
		}
		for _, r := range results {
			fmt.Fprintln(stdout, r.String())
		}
		dbv, err := provider.GetDBVersion(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "llm-broker migrate up: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "database version: %d\n", dbv)
		return exitOk
	}
	return exitUsage
}
