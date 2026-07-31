package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WorkerHeartbeat is the liveness row of an active worker.
type WorkerHeartbeat struct {
	WorkerID        uuid.UUID
	State           string
	StartedAt       time.Time
	LastHeartbeatAt time.Time
}

// UpsertWorkerHeartbeat records or refreshes a worker heartbeat.
func (s *Store) UpsertWorkerHeartbeat(ctx context.Context, workerID uuid.UUID, state string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO worker_heartbeats (worker_id, state, started_at, last_heartbeat_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (worker_id) DO UPDATE SET
			state = EXCLUDED.state,
			last_heartbeat_at = now()`,
		workerID, state)
	return err
}

// ListActiveWorkers returns worker heartbeats whose last heartbeat is newer
// than staleAfter.
func (s *Store) ListActiveWorkers(ctx context.Context, staleAfter time.Duration) ([]WorkerHeartbeat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT worker_id, state, started_at, last_heartbeat_at
		FROM worker_heartbeats
		WHERE state = 'active' AND last_heartbeat_at >= now() - ($1 * interval '1 second')`,
		int64(staleAfter.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkerHeartbeat
	for rows.Next() {
		var w WorkerHeartbeat
		if err := rows.Scan(&w.WorkerID, &w.State, &w.StartedAt, &w.LastHeartbeatAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PruneStaleWorkers deletes heartbeats older than olderThan and returns the
// number of removed rows.
func (s *Store) PruneStaleWorkers(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM worker_heartbeats
		WHERE last_heartbeat_at < now() - ($1 * interval '1 second')`,
		int64(olderThan.Seconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetWorkerHeartbeat returns a single worker heartbeat.
func (s *Store) GetWorkerHeartbeat(ctx context.Context, workerID uuid.UUID) (WorkerHeartbeat, error) {
	var w WorkerHeartbeat
	err := s.pool.QueryRow(ctx, `
		SELECT worker_id, state, started_at, last_heartbeat_at
		FROM worker_heartbeats WHERE worker_id = $1`, workerID).Scan(
		&w.WorkerID, &w.State, &w.StartedAt, &w.LastHeartbeatAt)
	if err == pgx.ErrNoRows {
		return WorkerHeartbeat{}, ErrNotFound
	}
	return w, err
}
