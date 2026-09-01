package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/JM-SE/llm-broker/internal/api"
	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// runAPI implements the `api` command: it starts the HTTP server and serves
// until the process receives a termination signal, then shuts down
// gracefully. The configuration path comes from $LLM_BROKER_CONFIG_FILE.
func runAPI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "" {
		fmt.Fprintln(stderr, "llm-broker api: takes no arguments; configuration comes from $LLM_BROKER_CONFIG_FILE")
		return exitUsage
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, st, ok := openStore(ctx, stderr)
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.CheckSchemaCompatible(ctx); err != nil {
		fmt.Fprintf(stderr, "llm-broker api: startup schema fence: %v\n", err)
		return 1
	}
	digest := cfg.TopologyDigest()
	configured := make([]postgres.ConfiguredWorkloadTopology, 0, len(cfg.Workloads))
	for slug := range cfg.Workloads {
		configured = append(configured, postgres.ConfiguredWorkloadTopology{Slug: slug, Digest: digest})
	}
	if err := st.CheckConfiguredTopology(ctx, configured); err != nil {
		fmt.Fprintf(stderr, "llm-broker api: startup policy fence: %v\n", err)
		return 1
	}
	if unmanaged, err := st.ListUnmanagedWorkloads(ctx, configuredWorkloadSlugs(cfg)); err != nil {
		fmt.Fprintf(stderr, "llm-broker api: startup policy inspection: %v\n", err)
		return 1
	} else if len(unmanaged) > 0 {
		logger.Warn("unmanaged durable workloads retained", "count", len(unmanaged))
	}

	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := st.GetClientByIdentifier(ctx, identifier)
		if errors.Is(err, postgres.ErrNotFound) {
			return auth.Identity{}, nil, false, nil
		}
		if err != nil {
			return auth.Identity{}, nil, false, err
		}
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})

	srv := api.New(api.Options{
		Store:    st,
		Config:   cfg,
		Verifier: verifier,
		Logger:   logger,
	})
	httpSrv := &http.Server{
		Addr:              cfg.API.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	logger.Info("api listening", "address", cfg.API.Listen)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			return 1
		}
		logger.Info("api stopped")
		return exitOk
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			return 1
		}
		return exitOk
	}
}
