package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/JM-SE/llm-broker/internal/metrics"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// RetentionJob periodically purges expired terminal payloads, results, and
// operational metadata in bounded batches. It is safe to restart: each run
// re-derives the cutoffs from the workload policy retention columns, and
// queued/running payloads are never eligible (cleanup selects only terminal
// tasks). Aggregate counts are logged and exposed as metrics.
type RetentionJob struct {
	Store    *postgres.Store
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	Interval time.Duration
	Batch    int
}

// Default retention cadence: the shortest default retention window is one
// hour, so a ten-minute sweep stays far inside the boundary.
const (
	DefaultRetentionInterval = 10 * time.Minute
	DefaultRetentionBatch    = 1000
)

// RunOnce performs one sweep and reports aggregate counts.
func (j *RetentionJob) RunOnce(ctx context.Context) error {
	counts, err := j.Store.CleanupExpired(ctx, j.Batch)
	if err != nil {
		return err
	}
	if j.Metrics != nil {
		j.Metrics.IncPurged(metrics.KindPayloads, counts.Payloads)
		j.Metrics.IncPurged(metrics.KindResults, counts.Results)
		j.Metrics.IncPurged(metrics.KindTasks, counts.Tasks)
	}
	if j.Log != nil {
		j.Log.Info("retention sweep",
			"event", "retention_sweep",
			"payloads", counts.Payloads,
			"results", counts.Results,
			"tasks", counts.Tasks)
	}
	return nil
}

// Run executes periodic sweeps until ctx is canceled.
func (j *RetentionJob) Run(ctx context.Context) {
	if j.Interval <= 0 {
		j.Interval = DefaultRetentionInterval
	}
	if j.Batch <= 0 {
		j.Batch = DefaultRetentionBatch
	}
	if j.Log == nil {
		j.Log = slog.Default()
	}
	ticker := time.NewTicker(j.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err := j.RunOnce(runCtx)
			cancel()
			if err != nil {
				j.Log.Error("retention sweep failed",
					"event", "error", "error", err)
			}
		}
	}
}

// QueueStatsLoop refreshes the bounded queue gauges on a fixed cadence.
type QueueStatsLoop struct {
	Store   *postgres.Store
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Every   time.Duration
}

const defaultQueueStatsInterval = 5 * time.Second

// Run refreshes gauges until ctx is canceled.
func (q *QueueStatsLoop) Run(ctx context.Context) {
	if q.Every <= 0 {
		q.Every = defaultQueueStatsInterval
	}
	ticker := time.NewTicker(q.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := q.Store.QueueStats(ctx)
			if err != nil {
				if q.Log != nil {
					q.Log.Error("queue stats refresh failed", "event", "error", "error", err)
				}
				continue
			}
			if q.Metrics != nil {
				q.Metrics.RefreshQueue(stats.Queued, stats.Running, stats.OldestQueuedSeconds)
			}
		}
	}
}
