# Product Phase P04: API, Authentication, And Idempotency

## Status

- Phase: P04.
- State: `accepted`.
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
- Token provisioning writes the credential only to an explicitly selected
  mode-0600 file and never prints the plaintext token.
- API binds loopback by default.

## Stop Conditions

- Any endpoint can execute inference or select backend/model.
- Authorization is enforced only in application code after an unscoped query.
- Wait path bypasses durable submission.
- Error/log output reveals sensitive or provider-specific material.
- API startup silently migrates schema.

## Handoff

### Changed Files

- `internal/auth/`: credential generation, SHA-256 digest, identifier
  extraction, constant-time verification with dummy-digest comparison —
  `token.go`, `verifier.go`, `verifier_test.go`.
- `internal/api/`: HTTP surface — `server.go` (routing/middleware chain),
  `middleware.go` (request id, panic recovery, sanitized logging, bearer
  auth), `tasks.go` (create/wait/read/result/cancel, generation policy,
  result envelope mapping), `ready.go` (health/readiness), `problems.go`
  (RFC 7807 + stable codes), plus `harness_test.go` (disposable PostgreSQL
  TestMain), `tasks_test.go`, `middleware_test.go`.
- `internal/store/postgres/`: `credential_identifier` column and
  `GetClientByIdentifier`/`RotateClientCredential`; task `cancel_requested_at`
  with scoped `GetTaskByClient`/`CancelQueuedTask`/`RequestCancelRunning`;
  `GetResultByTaskID`, `GetLastAttempt`, `CountActiveTasks`, `SaveResult`,
  `MarkAttemptFailed`, `GetWorkloadPolicyByID`; migration
  `00002_task_cancel_request.sql` (cancel intent + credential identifier).
- `internal/domain/`: `DecodeTaskSubmission` now rejects trailing content via
  `ErrTrailingContent` sentinel (HTTP 400 vs 422 classification).
- `cmd/llm-broker/`: `api.go` (server command with graceful shutdown),
  `client.go` (create/disable/revoke/grant), `dbutil.go` (shared store
  opening); `main.go` wiring and usage; `main_test.go` reduced to the worker
  placeholder.
- `openapi.yaml`: POST `/v1/tasks` gains the `200` terminal-within-wait
  response; `tests/openapi_test.go` pins the wait contract.
- `docs/api.md`: operator setup, curl transcript, operational notes.

### Validation Evidence

- `go test ./... -count=1`: all packages pass (integration suites run against
  disposable PostgreSQL 17 on loopback 55432).
- `go test -race ./...`: all packages pass.
- Concurrency: 16 same-key POSTs yield exactly one task ID; cross-client keys
  are independent; 12 concurrent claims claim each row once (P03 regression).
- Protocol: 201/replay/409 idempotency, 401 (missing/malformed/wrong/disabled),
  403 (unknown/ungranted workload), 422 (unknown field, capability,
  generation policy, json_schema without schema), 429 admission, 400 (bad
  body, trailing content, bad Prefer), 202 wait expiry, 200 terminal wait,
  disconnect leaves task queued, scoped reads/cancels are 404 cross-client,
  cancel idempotent/conflict/intent-recorded.
- OpenAPI document validates; wait contract pinned; product/source scans
  clean; secret scan clean; `gofmt -l` empty; `go vet` clean; `go build ./...`
  ok; `go mod verify` ok.
- Fuzz (3s each): domain/config fuzzers pass (no crashes).
- Manual smoke against the disposable database: full curl transcript in
  `docs/api.md` — create/replay/401/wait-202/read/result/cancel/disable,
  sanitized request logs verified, graceful shutdown verified.

### Compatibility Questions

- Workload policy rows are read from the database at submission time; there
  is no command yet to sync them from configuration. The smoke run seeded the
  policy with SQL. Consider a `sync` command in P05 (scheduler) scope.
- `task_results`/attempt-outcome persistence methods (`SaveResult`,
  `MarkAttemptFailed`) were introduced in P04 for the API tests; P07 (worker)
  will use them as-is or adjust signatures.
- Migration `00002` combines cancel intent and credential identifier; it is
  in-flight (never released), so it was not split into two migrations.
- Cancel of a running task records `cancel_requested_at`; the worker (P07)
  defines the exact discard semantics.

### Suggested Commit Message

`feat: P04 API, authentication, and idempotency (HTTP surface, bearer credentials, bounded wait, cancel intent)`

Stop for approval before P05.
