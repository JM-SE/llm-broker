-- Seed the operator-defined workload policy matching deploy/compose/config.json.
-- Run once after `migrate up`:
--   docker compose exec -T db psql -U llm_broker -d llm_broker < deploy/compose/seed.sql
INSERT INTO workload_policies (
    name, priority_class, max_active_tasks, max_attempts, retry_delays_seconds,
    max_output_tokens, allowed_generation, required_features,
    payload_retention_seconds, result_retention_seconds, metadata_retention_seconds,
    workload_timeout_seconds)
VALUES (
    'default', 'normal', 10, 3, '{1,5}', 2048, '{}', '{}',
    3600, 604800, 2592000, 300)
ON CONFLICT (name) DO UPDATE SET
    priority_class = EXCLUDED.priority_class,
    max_active_tasks = EXCLUDED.max_active_tasks,
    max_attempts = EXCLUDED.max_attempts,
    retry_delays_seconds = EXCLUDED.retry_delays_seconds,
    max_output_tokens = EXCLUDED.max_output_tokens,
    allowed_generation = EXCLUDED.allowed_generation,
    required_features = EXCLUDED.required_features,
    payload_retention_seconds = EXCLUDED.payload_retention_seconds,
    result_retention_seconds = EXCLUDED.result_retention_seconds,
    metadata_retention_seconds = EXCLUDED.metadata_retention_seconds,
    workload_timeout_seconds = EXCLUDED.workload_timeout_seconds,
    updated_at = now();
