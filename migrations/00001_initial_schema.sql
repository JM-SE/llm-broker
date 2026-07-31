-- +goose Up
-- Initial broker schema: identity, policies, tasks, payloads, results,
-- attempts, and worker heartbeats.
--
-- Design notes:
--   - Tasks carry only operational metadata plus the idempotency key and the
--     request hash. The normalized request body lives in task_payloads and the
--     output lives in task_results so that sensitive data can be retained,
--     purged, and access-controlled independently from the task lifecycle.
--   - State/timestamp consistency is enforced by database constraints so an
--     invalid transition cannot be persisted even if application code errs.
--   - Retention durations are per workload policy, so cleanup honors the
--     operator-defined window for each workload.

CREATE TABLE clients (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              TEXT NOT NULL,
    credential_digest BYTEA NOT NULL CHECK (octet_length(credential_digest) = 32),
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT clients_name_unique UNIQUE (name)
);

CREATE TABLE workload_policies (
    id                         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                       TEXT NOT NULL,
    priority_class             TEXT NOT NULL CHECK (priority_class IN ('high', 'normal', 'batch')),
    max_active_tasks           INTEGER NOT NULL CHECK (max_active_tasks BETWEEN 1 AND 1000),
    max_attempts               INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 5),
    retry_delays_seconds       INTEGER[] NOT NULL DEFAULT '{}'::INTEGER[],
    max_output_tokens          INTEGER NOT NULL CHECK (max_output_tokens BETWEEN 1 AND 8192),
    allowed_generation         JSONB NOT NULL DEFAULT '{}'::JSONB,
    required_features          TEXT[] NOT NULL DEFAULT '{}'::TEXT[],
    payload_retention_seconds  BIGINT NOT NULL CHECK (payload_retention_seconds > 0),
    result_retention_seconds   BIGINT NOT NULL CHECK (result_retention_seconds > 0),
    metadata_retention_seconds BIGINT NOT NULL CHECK (metadata_retention_seconds > 0),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT workload_policies_name_unique UNIQUE (name),
    CONSTRAINT workload_retention_order CHECK (
        payload_retention_seconds <= result_retention_seconds
        AND result_retention_seconds <= metadata_retention_seconds
    )
);

CREATE TABLE client_workload_grants (
    client_id   BIGINT NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    workload_id BIGINT NOT NULL REFERENCES workload_policies (id) ON DELETE CASCADE,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, workload_id)
);

CREATE TABLE tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id       BIGINT NOT NULL REFERENCES clients (id) ON DELETE RESTRICT,
    workload_id     BIGINT NOT NULL REFERENCES workload_policies (id) ON DELETE RESTRICT,
    capability      TEXT NOT NULL CHECK (capability ~ '^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$'),
    idempotency_key TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 200),
    request_hash    BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    state           TEXT NOT NULL DEFAULT 'queued'
                    CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until     TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tasks_client_idempotency_unique UNIQUE (client_id, idempotency_key),
    CONSTRAINT tasks_state_timestamps CHECK (
        (state = 'running') = (started_at IS NOT NULL)
        AND ((state IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL))
        AND (lease_until IS NULL OR state = 'running')
    )
);

CREATE TABLE task_payloads (
    task_id    UUID PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_results (
    task_id                  UUID PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    content                  TEXT NOT NULL,
    finish_reason            TEXT NOT NULL
                             CHECK (finish_reason IN ('stop', 'length', 'content_filter', 'unknown')),
    usage_prompt_tokens      BIGINT NOT NULL DEFAULT 0 CHECK (usage_prompt_tokens >= 0),
    usage_completion_tokens  BIGINT NOT NULL DEFAULT 0 CHECK (usage_completion_tokens >= 0),
    usage_total_tokens       BIGINT NOT NULL DEFAULT 0 CHECK (usage_total_tokens >= 0),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE task_attempts (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id     UUID NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    attempt     INTEGER NOT NULL CHECK (attempt > 0),
    worker_id   UUID NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('claimed', 'succeeded', 'failed')),
    error_class TEXT,
    error_code  TEXT,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    CONSTRAINT task_attempts_task_attempt_unique UNIQUE (task_id, attempt),
    CONSTRAINT task_attempts_error_class_known CHECK (
        error_class IS NULL
        OR error_class IN (
            'client_invalid', 'policy_rejected', 'backend_rejected', 'output_invalid',
            'transient_unsent', 'transient_safe', 'indeterminate', 'internal'
        )
    )
);

CREATE TABLE worker_heartbeats (
    worker_id         UUID PRIMARY KEY,
    state             TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'stopped')),
    started_at        TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX tasks_client_created_idx ON tasks (client_id, created_at DESC);
CREATE INDEX tasks_queued_created_idx ON tasks (state, created_at) WHERE state = 'queued';
CREATE INDEX tasks_finished_at_idx ON tasks (finished_at) WHERE finished_at IS NOT NULL;
CREATE INDEX task_attempts_task_idx ON task_attempts (task_id, attempt);

-- +goose Down
DROP TABLE worker_heartbeats;
DROP TABLE task_attempts;
DROP TABLE task_results;
DROP TABLE task_payloads;
DROP TABLE tasks;
DROP TABLE client_workload_grants;
DROP TABLE workload_policies;
DROP TABLE clients;
