package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// openStore resolves the configuration from $LLM_BROKER_CONFIG_FILE, reads
// the database URL from the configured environment variable, and opens the
// runtime store. Used by the api and client commands.
func openStore(ctx context.Context, stderr io.Writer) (*config.Config, *postgres.Store, bool) {
	path, ok := resolveConfigPath(nil, stderr)
	if !ok {
		return nil, nil, false
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return nil, nil, false
	}
	url := os.Getenv(cfg.Database.URLEnv)
	if url == "" {
		fmt.Fprintf(stderr, "llm-broker: environment variable %s is not set\n", cfg.Database.URLEnv)
		return nil, nil, false
	}
	st, err := postgres.Open(ctx, url, postgres.Options{
		MaxConns:       int32(cfg.Database.MaxConnections),
		ConnectTimeout: time.Duration(cfg.Database.ConnectTimeoutSeconds) * time.Second,
	})
	if err != nil {
		fmt.Fprintf(stderr, "llm-broker: database connection: %v\n", err)
		return nil, nil, false
	}
	return cfg, st, true
}
