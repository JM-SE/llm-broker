# Observability

## Metrics

Expose Prometheus metrics with `api.metrics_enabled: true` in the
configuration (off by default; only on loopback interfaces). The endpoint
`GET /metrics` serves the text format from an in-process registry.

Every label set is fixed and operator-bounded: client identities, task
identifiers, model names, URLs, prompts, and raw errors never appear as
labels.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `llm_broker_http_requests_total` | counter | method, route, status | API requests by fixed route pattern |
| `llm_broker_http_request_duration_seconds` | histogram | method, route | API latency |
| `llm_broker_auth_failures_total` | counter | reason (`missing`/`invalid`) | authentication failures |
| `llm_broker_ready` | gauge | — | 1 when /ready reports ready |
| `llm_broker_tasks_queued` | gauge | — | queued tasks (worker refresh) |
| `llm_broker_tasks_running` | gauge | — | running tasks; product invariant keeps it at 0 or 1 |
| `llm_broker_oldest_queued_task_seconds` | gauge | — | age of the oldest queued task (starvation signal) |
| `llm_broker_scheduling_decisions_total` | counter | reason (`batch_opened`/`batch_continued`) | claim decisions |
| `llm_broker_inference_attempts_total` | counter | class | outcomes: `succeeded`, `cancelled`, or a stable error class |
| `llm_broker_worker_leadership` | gauge | — | 1 while this process holds the execution lock |
| `llm_broker_retention_purged_total` | counter | kind (`payloads`/`results`/`tasks`) | rows purged by the retention job |

Route labels are stable names (`health`, `ready`, `metrics`,
`tasks.create`, `tasks.get`, `tasks.result`, `tasks.cancel`); raw paths with
task identifiers are never emitted.

Alerting guidance:

- `llm_broker_ready == 0` for more than a few minutes.
- `llm_broker_worker_leadership` flapping (lock churn).
- `llm_broker_oldest_queued_task_seconds` growing past aging bounds.
- `llm_broker_inference_attempts_total{class="indeterminate"}` > 0: an
  ambiguous execution blocks all inference until operator resolution.

## Logs

Structured logs (slog text handler) contain only: event name, request id,
route label, status, duration, and operational counters. Correlation is
done through `X-Request-Id` (echoed in responses and log lines) or the
operator-supplied non-sensitive trace metadata; task identifiers appear in
worker logs for operational correlation but never in API logs.

Stable event names: `request`, `panic`, `error`, `retention_sweep`,
`task succeeded`, `retrying task after transient failure`, `attempt budget
exhausted`, `indeterminate outcome`, `worker blocked`, `worker active`,
`worker standing by`, `worker stopping`.

## Readiness

`GET /ready` returns 503 unless: the database is reachable and
schema-compatible, an active worker holds the execution lock (fresh
heartbeat), and no ambiguous execution is unresolved. `GET /health` is
process liveness only.
