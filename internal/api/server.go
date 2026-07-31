package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// Options wires the server to its dependencies.
type Options struct {
	Store    *postgres.Store
	Config   *config.Config
	Verifier *auth.Verifier
	Logger   *slog.Logger
}

// Server implements the client-facing HTTP API. It validates, authenticates,
// and persists; it never selects backends, schedules, or executes inference.
type Server struct {
	store    *postgres.Store
	cfg      *config.Config
	verifier *auth.Verifier
	log      *slog.Logger
}

// New builds a server. Logger defaults to slog.Default when nil.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{
		store:    opts.Store,
		cfg:      opts.Config,
		verifier: opts.Verifier,
		log:      opts.Logger,
	}
}

// Handler returns the routed handler with the full middleware chain:
// request id, panic recovery, sanitized request logging, then per-route
// authentication.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.Handle("POST /v1/tasks", s.requireAuth(http.HandlerFunc(s.handleCreateTask)))
	mux.Handle("GET /v1/tasks/{task_id}", s.requireAuth(http.HandlerFunc(s.handleGetTask)))
	mux.Handle("GET /v1/tasks/{task_id}/result", s.requireAuth(http.HandlerFunc(s.handleGetResult)))
	mux.Handle("POST /v1/tasks/{task_id}/cancel", s.requireAuth(http.HandlerFunc(s.handleCancelTask)))
	return requestID(recoverPanic(logRequests(mux, s.log), s.log))
}

// lookupClient adapts the store to the auth verifier. Unknown identifiers
// yield a nil digest so verification cost does not reveal client existence.
func (s *Server) lookupClient(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
	c, err := s.store.GetClientByIdentifier(ctx, identifier)
	if errors.Is(err, postgres.ErrNotFound) {
		return auth.Identity{}, nil, false, nil
	}
	if err != nil {
		return auth.Identity{}, nil, false, err
	}
	return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
}
