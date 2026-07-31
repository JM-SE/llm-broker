# LLM Broker v1 Master Specification

## Status

- Document status: `ready_for_review`.
- Product status: not implemented.
- Integration status: no runtime or client is connected.
- This document becomes the source of truth only after explicit approval of
  Product Phase P00.

## Authority And Change Control

- This document defines the public product contract and architecture.
- `specs/implementation/00_implementation_phase_index.spec.md` defines execution
  order for the product and integration tracks.
- Product phase specs may not introduce a dependency on a named application,
  model, local path, host, or runtime vendor.
- Integration specs may select concrete products but must remain sanitized and
  must not change the public product contract silently.
- Every phase stops in `ready_for_review`; no phase self-approves.
- Git commits are never automatic. A phase handoff may suggest a commit message.
- Product, persistence, API, scheduling, privacy, compatibility, or rollout
  changes require a visible spec revision before implementation.

## Product Identity

LLM Broker is a durable, single-host inference scheduler for applications that
share local or privately hosted language-model servers. It is an independent
infrastructure application, not an extension of any client application, model,
or runtime.

The public v1 deliberately serializes all inference globally. This is a product
guarantee, not a deployment accident. Resource pools and parallel inference are
future architecture work.

## Objective

Allow independent applications to submit persistent text-generation tasks while
the broker provides:

- Authentication and client isolation.
- Durable queue and result state.
- Persistent idempotency.
- Broker-owned scheduling policy.
- Priority aging and starvation prevention.
- Bounded backend-affinity batches.
- Exactly one active inference globally.
- Safe retry and indeterminate-outcome handling.
- Logical capability routing without exposing concrete models.
- Privacy-aware retention, logs, and metrics.
- A generic backend boundary with built-in OpenAI-compatible support.

Client applications retain prompts, business logic, deterministic validation,
domain state, idempotency-key generation, and result consumption.

## Public V1 Scope

- One logical broker deployment.
- One API process and one active worker process from the same Go binary.
- PostgreSQL as the durable source of truth.
- One inference globally at any time, even when multiple backends are configured.
- Asynchronous durable task API with optional bounded synchronous wait.
- Operator-defined clients, workloads, capabilities, backends, and model aliases.
- Built-in `fake` backend adapter.
- Built-in `openai_compatible` backend adapter for Chat Completions semantics.
- A documented Go adapter interface for future protocols.
- Optional runtime-controller interface for provider-specific idleness and
  fencing support.
- Text messages and text/JSON/JSON-Schema final responses.
- Binary and container distribution.
- Docker Compose example with PostgreSQL.
- Sanitized deployment and integration examples.

## Non-Goals

- No distributed consensus or high availability.
- No parallel inference, resource pools, or multi-GPU scheduling in v1.
- No inference preemption.
- No streaming or WebSocket API in v1.
- No tools/function calling, images, audio, or embeddings in v1.
- No guarantee of compatibility with a protocol that has no adapter.
- No dynamic Go plugins or execution of third-party code at runtime.
- No Redis, Celery, external queue, ORM, or AI framework.
- No client-supplied backend URL, concrete model, model path, system command, or
  policy priority.
- No prompt construction or domain-specific validation in the broker.
- No public administration HTTP API.
- No automatic database migrations at API or worker startup.
- No chain-of-thought storage or exposure.

## System Invariants

1. Every inference begins as a committed broker task.
2. The API process never executes inference.
3. Only the active worker invokes backend adapters.
4. The worker starts no inference without the global PostgreSQL advisory lock.
5. At most one backend `Infer` call is active globally.
6. Scheduling and cancellation take effect only between inference calls.
7. A running inference is never preempted.
8. Client identity comes only from authenticated credentials.
9. Client reads and cancellation are scoped to that identity.
10. Clients submit workload and capability names, never backend/model identity or
    numeric priority.
11. Backends and capabilities are operator configuration.
12. `reasoning_content` or equivalent hidden reasoning is discarded and never
    logged, persisted, or returned.
13. An indeterminate backend outcome is never retried automatically.
14. An expired lease alone does not prove that backend execution stopped.
15. No new inference begins until backend idleness is proven or the backend is
    fenced and recovery is explicitly resolved.
16. The broker guarantees one simultaneous outbound inference; it cannot prevent
    actors that bypass the broker from calling the same runtime.
17. Logs and metrics contain no prompts, full outputs, credentials, raw schemas,
    idempotency keys, or arbitrary backend error text.

## Agnostic Boundaries

### Client Boundary

The public task contract uses generic workload, capability, messages, generation
policy, response contract, and trace metadata. No client-specific entity or
workflow appears in the broker schema or API.

### Backend Boundary

Inference protocol behavior is implemented by adapters. The broker core does not
assume a runtime product, local model loader, executable path, or model file.

The public compatibility statement is:

> LLM Broker v1 supports tested OpenAI-compatible Chat Completions endpoints and
> can be extended with compiled Go adapters for other protocols.

OpenAI-compatible implementations vary. Compatibility is feature-tested and
documented rather than inferred solely from endpoint naming.

### Runtime-Control Boundary

An inference adapter sends requests and parses responses. An optional runtime
controller may report activity, prove idleness, or fence/restart a runtime.

- `none`: portable default. Indeterminate recovery requires operator evidence.
- Provider-specific controller: optional integration that can strengthen
  recovery evidence without changing task or scheduling semantics.

The core never assumes that closing an HTTP connection stops generation.

### Deployment Boundary

The public product contains no personal filesystem path, occupied host port,
private credential, or required systemd layout. Binary, Docker, Compose, and
systemd examples are parameterized. Concrete deployments provide values outside
version control.

## Architecture

### Processes And Commands

One `llm-broker` binary provides:

- `llm-broker api`: client API, health, readiness, and metrics.
- `llm-broker worker`: scheduling, claims, backend calls, recovery, and retention.
- `llm-broker migrate`: explicit migration status and application.
- `llm-broker client`: offline credential and workload-grant administration.
- `llm-broker config`: configuration validation and redacted inspection.

API and worker are separate OS processes. The API never starts an in-process
background worker. Multiple worker processes may be started for standby, but
only the advisory-lock owner may become active.

### Go Stack

- Go toolchain: 1.26.5 for initial development.
- Module path: `github.com/JM-SE/llm-broker`.
- Standard library for HTTP, JSON, context, crypto, flags, signals, and `slog`.
- PostgreSQL: `github.com/jackc/pgx/v5` and `pgxpool`.
- Migrations: `github.com/pressly/goose/v3` with embedded SQL.
- JSON Schema: `github.com/santhosh-tekuri/jsonschema`, Draft 2020-12.
- Metrics: `github.com/prometheus/client_golang`.
- No HTTP framework, ORM, command framework, or dependency-injection framework.
- Stable direct dependencies are compatibility-tested and pinned in `go.mod` and
  `go.sum`; beta releases are prohibited.

### Repository Layout

```text
cmd/llm-broker/
internal/api/
internal/auth/
internal/backend/
internal/config/
internal/domain/
internal/metrics/
internal/scheduler/
internal/store/postgres/
internal/worker/
migrations/
deploy/examples/
docs/backends/
docs/integrations/
specs/implementation/product/
specs/implementation/integrations/
tests/
openapi.yaml
Dockerfile
compose.example.yaml
go.mod
go.sum
```

Package boundaries may be simplified when this removes unnecessary abstraction
without coupling core behavior to an integration.

## Configuration Contract

### Sources

- Non-secret structured configuration uses a versioned JSON file.
- The path is selected by `LLM_BROKER_CONFIG_FILE` or a command flag.
- Secrets and deployment-specific credentials come from environment variables or
  mounted secret files, never literal public examples.
- A small documented set of scalar environment overrides may exist for container
  operation.
- `llm-broker config validate` validates without contacting a backend.
- `llm-broker config show` is redacted and never prints secret values.

### Generic Model

Configuration defines:

- API listener and limits.
- PostgreSQL connection reference and pool limits.
- Retention and maintenance policy.
- Named backend instances.
- Named logical capabilities.
- Workload-policy defaults and hard bounds.
- Backend feature declarations.

A sanitized conceptual example is:

```json
{
  "version": 1,
  "backends": {
    "primary": {
      "type": "openai_compatible",
      "base_url": "http://llm-runtime:8080/v1",
      "model": "operator-model-alias",
      "credential_env": "LLM_BACKEND_API_KEY"
    }
  },
  "capabilities": {
    "instruct": {
      "backend": "primary"
    }
  }
}
```

Backend names and model values are operator-only. They never appear in the task
submission schema or client-visible result.

### Safe Startup

- No usable production backend is implicit.
- Development/test configuration defaults to `fake` only.
- Unknown config keys, adapter types, capabilities, and unsafe combinations fail
  startup.
- URL validation rejects embedded credentials and unsupported schemes.
- Public bind addresses require explicit configuration and authentication.
- Configuration validation never makes network calls.

## Backend Adapter Contract

### Responsibilities

An adapter:

- Validates its operator configuration.
- Declares supported optional features.
- Converts a normalized broker request into provider protocol.
- Uses explicit connect, write, read, pool, and overall deadlines.
- Parses normalized content, usage, and finish reason.
- Separates and discards hidden reasoning.
- Classifies failures conservatively.
- Never logs raw request or response bodies.

The core owns retries, state transitions, scheduling, authorization, and result
persistence. Adapters do not update task state directly.

### Built-In Fake Adapter

The `fake` adapter is deterministic and supports configurable delay, success,
rejection, malformed output, definitely-unsent failure, safe transient failure,
and indeterminate outcome. It performs no network request.

### Built-In OpenAI-Compatible Adapter

The `openai_compatible` adapter targets Chat Completions-compatible endpoints.
It supports the broker message roles, allowlisted generation controls, optional
structured output when declared, bearer/API-key configuration, and normalized
response parsing.

- The adapter does not assume all providers implement every parameter.
- Operator config declares supported features and parameter mappings.
- Unsupported requested workload policy fails before task execution.
- Redirects are disabled by default.
- Credentials are sent only to the configured origin.
- Endpoint/model values remain operator-owned.
- Local and private HTTP endpoints are supported; TLS is expected for remote
  network deployments.

### Adapter Extension

New protocols implement the documented Go interface and register at compile
time. Each adapter requires contract tests for configuration, request mapping,
response parsing, timeout, error classification, reasoning removal, and log
redaction. Runtime-loaded Go plugins are prohibited in v1.

## API Contract

### Authentication

- Task endpoints require `Authorization: Bearer <credential>`.
- Credentials are random high-entropy values provisioned out of band.
- The database stores a non-secret identifier/prefix and SHA-256 digest, not the
  recoverable credential.
- Credentials have enabled, disabled, and revoked states.
- Digest comparison is constant time.
- Client identity, priority, backend, and authorization scope are never trusted
  from a request body.

### Endpoints

- `POST /v1/tasks`
- `GET /v1/tasks/{task_id}`
- `GET /v1/tasks/{task_id}/result`
- `POST /v1/tasks/{task_id}/cancel`
- `GET /health`
- `GET /ready`
- `GET /metrics`

There is no direct `/v1/chat/completions` client compatibility endpoint. The
broker task API remains durable and asynchronous.

### Submission

`POST /v1/tasks` requires an `Idempotency-Key` scoped to the authenticated client.
The strict body contains:

- `workload`: operator-granted logical workload identifier.
- `capability`: operator-defined logical capability identifier.
- `messages`: system, user, and assistant text messages.
- `generation`: allowlisted normalized generation controls.
- `response`: text, JSON object, or constrained JSON Schema contract.
- `trace`: optional non-sensitive scalar correlation metadata.

The v1 normalized generation vocabulary is `max_tokens`, `temperature`, `top_p`,
`top_k`, `min_p`, `seed`, and `stop`. Workload policy controls defaults, allowed
ranges, backend compatibility, and hard limits. Unknown fields are rejected.

The idempotency record stores a deterministic hash of the normalized validated
request. Same key plus same request returns the original task. Same key plus a
different request returns conflict.

### Optional Wait

- `Prefer: wait=N` accepts integer seconds from 0 through 30.
- The task is committed before waiting.
- A terminal task may be returned during the wait.
- A non-terminal task returns `202 Accepted`, `Location`, and `Retry-After`.
- Client disconnect ends only the wait, not the task.
- Wait never bypasses persistence or scheduling.

### Error Semantics

- Errors use `application/problem+json` with stable broker codes.
- Authentication failure: `401`.
- Disabled credential or missing grant: `403`.
- Unknown or cross-client task: `404`.
- Idempotency or invalid state conflict: `409`.
- Admission limit: `429`.
- Invalid input: documented `400` or `422`.
- Not reliably ready: `503`.
- Raw provider errors and backend identity are not returned to clients.

## Task State Model

States are `queued`, `running`, `succeeded`, `failed`, and `cancelled`.

Allowed transitions are:

- `queued -> running`
- `queued -> cancelled`
- `running -> queued` only for a proven safe retry
- `running -> succeeded`
- `running -> failed`
- `running -> cancelled` only after inference completes and output is discarded

Terminal states never transition. Updates are transactional and include expected
current-state predicates. Queued cancellation is immediate. Running cancellation
never preempts the backend.

## Persistence Model

Core tables are:

- `clients`
- `workload_policies`
- `client_workload_grants`
- `tasks`
- `task_payloads`
- `task_results`
- `task_attempts`
- `worker_heartbeats`

PostgreSQL uses `timestamptz` in UTC. Task IDs are UUIDs. Constraints enforce
states, positive limits, scoped idempotency, and referential integrity.

Migrations are embedded and run only through `llm-broker migrate`. API and worker
readiness require the expected schema version. PostgreSQL may be operator-managed
or supplied by the example Compose stack; the product does not require a named
container, host port, or local volume path.

Payload, result, and operational metadata are separated. No raw backend envelope,
authorization header, duplicate prompt in operational tables, or reasoning is
persisted.

## Scheduling Policy

Initial broker-owned classes are:

| Class | Base priority |
| --- | ---: |
| `high` | 300 |
| `normal` | 200 |
| `batch` | 100 |

- Clients submit workload, not priority.
- Effective priority gains 100 every 10 queued minutes, capped at 300.
- Ties use `created_at`, then task ID.
- Only eligible queued tasks participate.
- Selection and claim are atomic with row locks.
- `FOR UPDATE SKIP LOCKED` hardens claims but does not permit parallel inference.
- Backend/model affinity batches stop after 4 tasks or 10 minutes by default.
- A higher effective priority closes a batch after the current inference.
- Multiple configured backends remain globally serialized in v1.
- Admission defaults are 250 active tasks per client and 1000 globally.
- Values are operator-configurable within validated hard bounds.

The worker uses bounded polling and does not depend on notification delivery for
correctness.

## Worker Exclusion And Recovery

- A session-level PostgreSQL advisory lock selects the active worker.
- The lock uses a dedicated connection held for worker lifetime.
- A standby worker reports its role and never claims tasks.
- Claiming records worker, attempt, lease, and `running` atomically.
- Heartbeats refresh worker and active-task lease state.
- Shutdown stops new claims and drains the current request within its budget.
- Loss of worker, database, or backend connection enters recovery.

Lease expiry is not proof of backend idleness. An indeterminate attempt blocks
new inference until an optional runtime controller proves idle or an operator
fences the backend and records recovery through a non-client CLI workflow.

## Error And Retry Policy

Classes are:

- `client_invalid`
- `policy_rejected`
- `backend_rejected`
- `output_invalid`
- `transient_unsent`
- `transient_safe`
- `indeterminate`
- `internal`

Only `transient_unsent` and `transient_safe` retry automatically. The default
maximum is 3 attempts including the first. Base retry delays are 1 and 5 seconds
with bounded jitter. Indeterminate outcomes never retry automatically.

## Validation And Limits

Public defaults are safe and operator-configurable within hard bounds:

- HTTP body: 512 KiB.
- Combined message content: 256 KiB.
- Messages: 64.
- Trace metadata: 4 KiB and 16 scalar keys.
- Idempotency key: 8 through 200 printable ASCII characters.
- JSON Schema: 64 KiB, depth 16, 256 declared properties.
- JSON Schema: Draft 2020-12 only.
- Remote `$ref`, remote `$id`, executable extensions, and network loading are
  rejected.
- Global hard output limit: 8192 normalized tokens requested.

The broker does not tokenize for every provider unless an adapter explicitly
supports it. Clients remain responsible for domain context preparation; provider
context rejection is classified safely.

## Privacy And Retention

Default retention is:

- Terminal request payload: 1 hour.
- Sanitized result: 7 days.
- Operational and idempotency metadata: 30 days.

Operators may reduce or adjust retention within documented bounds. Cleanup is
bounded, transactional, repeatable, and owned by the worker. Prompts, results,
credentials, raw bodies, and reasoning never enter logs or metric labels.

## Health, Readiness, Metrics, And Logs

`/health` is process liveness. API readiness requires database connectivity and
expected schema version. Worker readiness additionally requires fresh heartbeat,
advisory-lock ownership for active role, valid adapter configuration, and no
unresolved indeterminate state.

Metrics cover HTTP, auth failures, submissions, task outcomes, queue depth/age,
scheduling latency, inference duration, attempts, classified failures, lock and
heartbeat state, backend adapter type, and retention deletion counts.

Labels are bounded. Client IDs, task IDs, keys, model values, URLs, prompt/output,
and arbitrary provider errors are prohibited labels. JSON logs use stable event
names and sanitized fields.

## Security Model

- Safe listener default is loopback.
- Public exposure requires explicit bind configuration, authentication, and
  operator-controlled network security/TLS termination.
- Backend credentials are separate from client credentials.
- Backend URLs and models are operator configuration only.
- Redirects and credential forwarding are restricted.
- Database credentials distinguish migration ownership from runtime access in
  production guidance.
- Container and systemd examples use least privilege and read-only filesystem
  controls where practical.
- No secret, private host path, or production identifier is committed.

## Open-Source Distribution

The public repository includes:

- Apache License 2.0.
- README with architecture, quick start, compatibility statement, and limits.
- `SECURITY.md` and responsible disclosure instructions.
- `CONTRIBUTING.md` and adapter development/testing guidance.
- Binary build instructions and version metadata.
- Multi-stage Dockerfile and non-root runtime image.
- Compose example with PostgreSQL and fake mode by default.
- Parameterized OpenAI-compatible and systemd examples.
- GitHub Actions for formatting, unit/integration tests, race tests, vet,
  vulnerability scanning, and reproducible build checks.
- Tagged release process with binaries, checksums, container image, and release
  notes.

No public quick start contacts a real model unless the operator explicitly adds
backend configuration.

## Integration Profiles

Integration profiles demonstrate use without redefining the product.

The initial sanitized profile covers:

- OpenAI-compatible inference through llama-swap.
- A Qwen Instruct model selected by operator configuration.
- One-model-resident runtime policy.
- Linux/systemd deployment example.
- An optional YTPipe client integration and controlled rollout.

Profile files use placeholders such as `<MODEL_PATH>`, `<BACKEND_API_KEY>`, and
`<CLIENT_CREDENTIAL>`. They contain no personal path, live port assumption,
secret, private database identity, or production payload. Exact host values live
outside Git.

Product v1 acceptance does not depend on successful deployment of any profile.
Integration tracks begin only after the generic product release candidate is
accepted.

## Testing Strategy

- Pure tests for state, policy, aging, batches, errors, limits, normalization,
  redaction, and capability resolution.
- PostgreSQL tests for migrations, constraints, claims, locks, leases,
  idempotency, retention, and concurrency.
- HTTP contract tests against `openapi.yaml`.
- Adapter conformance suite reused by built-in and future adapters.
- Fake end-to-end tests for all product acceptance.
- OpenAI-compatible adapter tests use `httptest`, not a real model.
- Fault injection covers pre-send failure, post-send disconnect, timeout,
  database loss, worker death, stale lease, cancellation, and duplicates.
- Tests require no public network or production data.
- Required checks include formatting, `go test`, race detector, `go vet`, build,
  vulnerability scan, container build, and Compose smoke test.

## Product V1 Acceptance

Product v1 is complete when Product Phases P00 through P09 are accepted and:

- A new user can run fake mode from binary or Compose documentation.
- A user can configure a tested OpenAI-compatible endpoint without code changes.
- A contributor can implement an adapter using documented contracts/tests.
- Multiple clients cannot exceed one global active inference.
- Queue, aging, idempotency, cancellation, retries, and recovery are durable.
- Sensitive data is absent from logs and metrics.
- Retention works without corrupting operational state.
- Build, container, migrations, backup/restore, and security checks pass.
- No product package or schema depends on a named client, model, runtime, host, or
  filesystem layout.

## Deferred Product Work

- Parallel inference and resource pools.
- Native adapters beyond OpenAI-compatible.
- Streaming, tools, multimodal input, embeddings, and reranking.
- Multiple hosts or active workers.
- Admin web API or UI.
- Client SDKs beyond examples.
- Dynamic plugins.
