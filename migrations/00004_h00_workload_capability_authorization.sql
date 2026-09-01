-- +goose Up
-- H00 is forward-only. Run it during planned downtime with every old API and
-- worker stopped. Existing authorization state is deliberately not inferred:
-- staged policy rows become deny-all and unprovisioned until policy apply.

ALTER TABLE workload_policies
    ADD COLUMN topology_digest BYTEA;
ALTER TABLE workload_policies
    ADD CONSTRAINT workload_policies_topology_digest_length
    CHECK (topology_digest IS NULL OR octet_length(topology_digest) = 32);

ALTER TABLE workload_policies
    ALTER COLUMN workload_timeout_seconds DROP DEFAULT;
ALTER TABLE workload_policies
    DROP CONSTRAINT workload_policies_workload_timeout_seconds_check;
ALTER TABLE workload_policies
    ALTER COLUMN workload_timeout_seconds SET NOT NULL;
ALTER TABLE workload_policies
    ADD CONSTRAINT workload_policies_timeout_range
    CHECK (workload_timeout_seconds BETWEEN 1 AND 3600);

CREATE TABLE workload_policy_capabilities (
    workload_id BIGINT NOT NULL REFERENCES workload_policies (id) ON DELETE CASCADE,
    capability TEXT NOT NULL
        CHECK (length(capability) BETWEEN 1 AND 64)
        CHECK (capability ~ '^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$'),
    PRIMARY KEY (workload_id, capability)
);

ALTER TABLE task_attempts
    DROP CONSTRAINT task_attempts_state_check;
ALTER TABLE task_attempts
    ADD CONSTRAINT task_attempts_state_check
    CHECK (state IN ('claimed', 'authorized', 'succeeded', 'failed'));

-- A legacy running task with a latest claimed attempt may have reached the
-- backend before downtime. It is therefore converted to authorized, the
-- conservative ambiguous state, rather than treated as safe-unsent.
UPDATE task_attempts ta
SET state = 'authorized'
FROM (
    SELECT t.id AS task_id, max(a.attempt) AS attempt
    FROM tasks t
    JOIN task_attempts a ON a.task_id = t.id
    WHERE t.state = 'running'
    GROUP BY t.id
) latest
WHERE ta.task_id = latest.task_id
  AND ta.attempt = latest.attempt
  AND ta.state = 'claimed';

CREATE INDEX tasks_active_client_workload_idx
    ON tasks (client_id, workload_id)
    WHERE state IN ('queued', 'running');

-- Role grants are conditional so a migration can be prepared before the
-- deployment-specific roles are created. The explicit role names are the
-- documented defaults; operators may grant the same contract to aliases.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llm_broker_provisioner') THEN
        GRANT USAGE ON SCHEMA public TO llm_broker_provisioner;
        GRANT SELECT ON goose_db_version TO llm_broker_provisioner;
        GRANT SELECT, INSERT ON workload_policies TO llm_broker_provisioner;
        GRANT UPDATE (priority_class, max_active_tasks, max_attempts,
            retry_delays_seconds, max_output_tokens, allowed_generation,
            required_features, workload_timeout_seconds, topology_digest,
            payload_retention_seconds, result_retention_seconds,
            metadata_retention_seconds, updated_at) ON workload_policies TO llm_broker_provisioner;
        GRANT SELECT, INSERT, DELETE ON workload_policy_capabilities TO llm_broker_provisioner;
        GRANT SELECT, INSERT, UPDATE, DELETE ON clients, client_workload_grants TO llm_broker_provisioner;
        GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO llm_broker_provisioner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llm_broker_runtime') THEN
        GRANT USAGE ON SCHEMA public TO llm_broker_runtime;
        GRANT SELECT ON goose_db_version TO llm_broker_runtime;
        GRANT SELECT ON clients, workload_policies, workload_policy_capabilities,
            client_workload_grants TO llm_broker_runtime;
        GRANT SELECT, INSERT ON tasks, task_payloads, task_attempts, task_results,
            worker_heartbeats TO llm_broker_runtime;
        GRANT DELETE ON tasks, task_payloads, task_results TO llm_broker_runtime;
        GRANT UPDATE (state, attempts, lease_until, started_at, finished_at,
            cancel_requested_at, retry_available_at, updated_at) ON tasks TO llm_broker_runtime;
        GRANT UPDATE (state, error_class, error_code, finished_at) ON task_attempts TO llm_broker_runtime;
        GRANT UPDATE (content, finish_reason, usage_prompt_tokens,
            usage_completion_tokens, usage_total_tokens) ON task_results TO llm_broker_runtime;
        GRANT UPDATE (state, last_heartbeat_at) ON worker_heartbeats TO llm_broker_runtime;
        -- Runtime inserts attempts, but must not advance identity sequences
        -- owned by clients or workload policy provisioning.
        GRANT USAGE, SELECT ON SEQUENCE task_attempts_id_seq TO llm_broker_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- H00 has no supported down migration. A rollback requires restoring the
-- database backup taken before planned downtime.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'H00 is forward-only; restore the pre-H00 backup instead';
END $$;
-- +goose StatementEnd
