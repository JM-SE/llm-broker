// Package postgres implements the broker store on PostgreSQL using pgx and
// explicit SQL. There is no ORM layer: every query is written and reviewed by
// hand, and correctness is enforced by database constraints plus integration
// tests against a disposable database.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionAdvisoryLockKey is the cluster-global advisory lock key the worker
// uses to enforce a single active worker across processes. It is a fixed
// constant ("LLMBROK" in ASCII), deliberately independent of schema names.
const SessionAdvisoryLockKey int64 = 0x4C4C4D42524F4B

// Sentinel store errors. Callers should compare with errors.Is.
var (
	// ErrNotFound indicates the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict indicates a uniqueness violation or an idempotency conflict.
	ErrConflict = errors.New("conflict")
	// ErrInvalidTransition indicates an expected-state predicate did not match.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrNoTaskAvailable indicates no queued task could be claimed.
	ErrNoTaskAvailable = errors.New("no queued task available")
	// ErrLockNotAcquired indicates the advisory lock could not be acquired
	// before the context deadline.
	ErrLockNotAcquired = errors.New("advisory lock not acquired")
	// ErrNotImplemented marks store operations a later phase owns.
	ErrNotImplemented = errors.New("not implemented")
)

// Options configures the connection pool.
type Options struct {
	// MaxConns bounds the pool size; zero uses the driver default.
	MaxConns int32
	// ConnectTimeout bounds establishing a connection; zero uses the driver
	// default.
	ConnectTimeout time.Duration
}

// Store is a PostgreSQL-backed store. It is safe for concurrent use: pgxpool
// manages connections and SQL row-level locking provides atomicity across
// processes, not just goroutines.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies connectivity with a ping.
func Open(ctx context.Context, url string, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.ConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return s, nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping verifies database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Pool exposes the underlying pool for tooling that needs a *sql.DB bridge
// (for example the migration runner).
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
