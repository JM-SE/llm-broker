package postgres

import (
	"context"
)

// QueueStats is a single-shot snapshot of queue depth, running tasks, and
// the age of the oldest queued task. Used by the worker to refresh bounded
// gauges.
type QueueStats struct {
	Queued              int64
	Running             int64
	OldestQueuedSeconds float64
}

// QueueStats returns the queue snapshot in one query.
func (s *Store) QueueStats(ctx context.Context) (QueueStats, error) {
	var st QueueStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM tasks WHERE state = 'queued'),
			(SELECT count(*) FROM tasks WHERE state = 'running'),
			COALESCE(
				(SELECT EXTRACT(EPOCH FROM (now() - min(created_at)))
				 FROM tasks WHERE state = 'queued'),
				0)`,
	).Scan(&st.Queued, &st.Running, &st.OldestQueuedSeconds)
	return st, err
}
