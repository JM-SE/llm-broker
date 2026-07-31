package postgres

import (
	"context"
)

// CleanupCounts reports rows removed by a cleanup sweep.
type CleanupCounts struct {
	Payloads int64
	Results  int64
	Tasks    int64
}

// CleanupExpired purges expired rows in bounded batches, honoring the
// per-workload retention windows:
//
//   - Payloads (the normalized request body) expire payload_retention_seconds
//     after the task finished; the task row keeps idempotency key and hash.
//   - Results expire result_retention_seconds after the task finished.
//   - Whole tasks (with payload/result/attempts via cascade) expire
//     metadata_retention_seconds after the task finished. Purging a task also
//     ends its idempotency window, which is the documented v1 semantic.
//
// Each sweep is bounded to batch rows per category so a single run never
// blocks the database.
func (s *Store) CleanupExpired(ctx context.Context, batch int) (CleanupCounts, error) {
	if batch <= 0 || batch > 10000 {
		batch = 1000
	}
	var counts CleanupCounts
	var err error

	counts.Payloads, err = s.purgePayloads(ctx, batch)
	if err != nil {
		return counts, err
	}
	counts.Results, err = s.purgeResults(ctx, batch)
	if err != nil {
		return counts, err
	}
	counts.Tasks, err = s.purgeTasks(ctx, batch)
	if err != nil {
		return counts, err
	}
	return counts, nil
}

func (s *Store) purgePayloads(ctx context.Context, batch int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH doomed AS (
			SELECT p.task_id
			FROM task_payloads p
			JOIN tasks t ON t.id = p.task_id
			JOIN workload_policies w ON w.id = t.workload_id
			WHERE t.finished_at IS NOT NULL
			  AND t.finished_at <= now() - (w.payload_retention_seconds * interval '1 second')
			LIMIT $1
		)
		DELETE FROM task_payloads WHERE task_id IN (SELECT task_id FROM doomed)`,
		batch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) purgeResults(ctx context.Context, batch int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH doomed AS (
			SELECT r.task_id
			FROM task_results r
			JOIN tasks t ON t.id = r.task_id
			JOIN workload_policies w ON w.id = t.workload_id
			WHERE t.finished_at IS NOT NULL
			  AND t.finished_at <= now() - (w.result_retention_seconds * interval '1 second')
			LIMIT $1
		)
		DELETE FROM task_results WHERE task_id IN (SELECT task_id FROM doomed)`,
		batch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) purgeTasks(ctx context.Context, batch int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH doomed AS (
			SELECT t.id
			FROM tasks t
			JOIN workload_policies w ON w.id = t.workload_id
			WHERE t.finished_at IS NOT NULL
			  AND t.finished_at <= now() - (w.metadata_retention_seconds * interval '1 second')
			LIMIT $1
		)
		DELETE FROM tasks WHERE id IN (SELECT id FROM doomed)`,
		batch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
