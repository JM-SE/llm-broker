package postgres

import (
	"context"
	"fmt"
)

// AcquireSessionAdvisoryLock takes a session-level PostgreSQL advisory lock on
// a dedicated pool connection and returns a release function. The connection
// is pinned for the whole duration of the lock: callers must invoke the
// release function exactly once, and the lock disappears automatically if the
// connection dies (which is the desired fail-safe for a worker lease).
//
// Only one session cluster-wide can hold the same key at a time, which is the
// primitive the worker uses to guarantee a single active worker across
// processes. The lock blocks until acquired or until ctx is done.
func (s *Store) AcquireSessionAdvisoryLock(ctx context.Context, key int64) (func() error, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lock connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		conn.Release()
		return nil, fmt.Errorf("pg_advisory_lock: %w", err)
	}
	release := func() error {
		defer conn.Release()
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key); err != nil {
			return fmt.Errorf("pg_advisory_unlock: %w", err)
		}
		return nil
	}
	return release, nil
}

// TryAcquireSessionAdvisoryLock acquires the advisory lock without blocking.
// It returns a nil release function and ErrLockNotAcquired when another
// session holds the lock.
func (s *Store) TryAcquireSessionAdvisoryLock(ctx context.Context, key int64) (func() error, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lock connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrLockNotAcquired
	}
	release := func() error {
		defer conn.Release()
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key); err != nil {
			return fmt.Errorf("pg_advisory_unlock: %w", err)
		}
		return nil
	}
	return release, nil
}
