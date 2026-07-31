# Product Phase P04: API, Authentication, And Idempotency

## Status

- Phase: P04.
- State: `pending`.
- Prerequisite: accepted P03.
- Runtime impact: isolated test API only; no worker or real backend.

## Objective

Expose the durable generic task API with strict authentication, client isolation,
admission, idempotency, cancellation, status, result, health, and readiness.

## In Scope

- Standard-library HTTP server and middleware.
- Bearer credential provisioning/verification.
- Workload grants and admission checks.
- POST task with optional bounded wait over persisted state.
- Task/result reads and cancellation.
- Problem Details errors, body/deadline limits, and graceful shutdown.
- API liveness/readiness and protocol-level tests.

## Out Of Scope

- Scheduling, worker, adapter invocation, or task completion.
- Admin HTTP API.
- OpenAI-compatible client facade.
- Production deployment or real client integration.

## Deliverables

- `internal/api` routing, middleware, handlers, and tests.
- `internal/auth` token generation/digest/constant-time verification.
- `llm-broker client create|disable|revoke|grant` offline commands.
- API server command and readiness checks.
- OpenAPI conformance tests.
- Integration tests with isolated PostgreSQL.

## Authentication Requirements

- Provisioned tokens are displayed once and never recoverable.
- Database stores token identifier/prefix and SHA-256 digest.
- Comparisons are constant time and avoid client-existence side channels where
  practical.
- Token, Authorization header, and full prefix are never logged.
- Disabled/revoked credentials cannot submit/read/cancel.
- Every task operation filters by authenticated client in SQL.

## Endpoint Requirements

- POST requires `Idempotency-Key` and persists before response/wait.
- `Prefer: wait=N` accepts only 0-30 and client disconnect cannot cancel task.
- Queued cancellation is immediate and idempotent.
- Running cancellation records intent; execution behavior arrives in P07.
- Cross-client and absent tasks both return `404`.
- Result status distinguishes pending, success, failed, cancelled, expired.
- Errors use stable codes and no backend/runtime details.
- Body parsing is bounded and rejects trailing/unknown JSON.

## Procedure

1. Implement secure credential creation and offline lifecycle commands.
2. Implement middleware order: request ID, recovery, limits/deadlines, auth,
   authorization, handler, sanitized logging/metrics hooks.
3. Implement POST transaction and idempotent replay/conflict behavior.
4. Implement bounded polling wait without holding a database transaction.
5. Implement scoped task/result/cancel reads and writes.
6. Implement health/readiness with schema/database checks only at this phase.
7. Add protocol, concurrency, timing, disconnect, and security tests.

## Automated Acceptance

- OpenAPI/handler contract tests pass.
- Concurrent same-key requests preserve one task.
- Different clients may reuse the same idempotency key independently.
- Cross-client reads/cancellation cannot reveal existence.
- Invalid auth/grant/input/admission/state map to specified status/codes.
- Wait returns terminal state when available and 202 when budget expires.
- Disconnect during wait leaves task queued.
- Logs contain no token, prompt, result, schema, or idempotency key.
- Fuzz, race, vet, build, database tests, and whitespace checks pass.

## Manual Acceptance

- `curl` examples work against an isolated test API.
- Task remains queued because no worker exists, proving API does not infer.
- Token provisioning output warns that the token is shown once.
- API binds loopback by default.

## Stop Conditions

- Any endpoint can execute inference or select backend/model.
- Authorization is enforced only in application code after an unscoped query.
- Wait path bypasses durable submission.
- Error/log output reveals sensitive or provider-specific material.
- API startup silently migrates schema.

## Handoff

Include endpoint/auth test evidence, sanitized example transcript, readiness
behavior, logs inspection, changed files, Git status, and a suggested commit
message. Stop for approval before P05.
