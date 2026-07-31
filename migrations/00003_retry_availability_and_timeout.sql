-- +goose Up
-- Persisted backoff: a retried task becomes eligible for claiming again only
-- at or after retry_available_at. Restart-safe by construction: the gate
-- lives in the row, not in worker memory. Only queued tasks may carry it.
ALTER TABLE tasks ADD COLUMN retry_available_at timestamptz;
ALTER TABLE tasks
    ADD CONSTRAINT tasks_retry_available_only_queued
    CHECK (retry_available_at IS NULL OR state = 'queued');

-- Per-workload execution deadline for a single attempt (seconds). The
-- worker bounds every adapter call with this timeout; hitting it yields an
-- indeterminate outcome because the backend may have accepted the request.
ALTER TABLE workload_policies
    ADD COLUMN workload_timeout_seconds integer NOT NULL DEFAULT 300
    CHECK (workload_timeout_seconds > 0);

-- +goose Down
ALTER TABLE workload_policies DROP COLUMN workload_timeout_seconds;
ALTER TABLE tasks DROP CONSTRAINT tasks_retry_available_only_queued;
ALTER TABLE tasks DROP COLUMN retry_available_at;
