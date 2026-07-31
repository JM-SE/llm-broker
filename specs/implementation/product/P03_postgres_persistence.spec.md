# Product Phase P03: PostgreSQL Persistence

## Status

- Phase: P03.
- State: `accepted`.
- Prerequisite: accepted P02.
- Runtime impact: test PostgreSQL only; no existing database is reused.

## Objective

Implement explicit portable migrations and a transactionally correct PostgreSQL
store for auth metadata, tasks, payloads, results, attempts, and worker state.

## In Scope

- Embedded Goose SQL migrations.
- Runtime and migration role guidance.
- Store queries with pgx/pgxpool and explicit SQL.
- Schema compatibility checks.
- Transactional state transitions and idempotency primitives.
- Claim/lease/advisory-lock primitives used by later phases.
- Retention query primitives.
- PostgreSQL integration tests in an isolated disposable database.

## Out Of Scope

- Reusing or modifying any application database.
- API endpoints, token verification, scheduling policy, adapter calls, or worker
  loops.
- Running migrations automatically at process startup.
- Production container/service creation.

## Deliverables

- `migrations/*.sql` with up/down behavior or documented irreversible boundary.
- `llm-broker migrate status|up` commands.
- `internal/store/postgres` with typed explicit queries.
- Test database harness and fixtures.
- Schema/backup/restore/operator documentation.

## Schema Requirements

- Tables: clients, workload policies, client grants, tasks, payloads, results,
  attempts, and worker heartbeats.
- UUID task identity and UTC `timestamptz`.
- Scoped unique client/idempotency constraint.
- Request hash retained independently of request payload retention.
- Payload/result separated from operational task row.
- Constraints enforce states, terminal timestamps, positive limits, and referential
  integrity.
- Attempts record sanitized class/timing and never raw provider bodies.
- Indexes support client reads, eligible scheduling, cleanup, and heartbeat checks.

## Transaction Requirements

- Task creation, payload insertion, and idempotency record are atomic.
- Same key/same request returns original task under concurrency.
- Same key/different hash produces conflict.
- State transitions use expected-state predicates.
- Claim is atomic and records lease/attempt/worker.
- Advisory lock uses a dedicated session connection.
- Cleanup uses bounded batches and preserves metadata needed for idempotency.

## Procedure

1. Provision a disposable test PostgreSQL matching supported versions.
2. Create least-privilege migration/runtime role fixtures.
3. Write and review migration SQL before applying it.
4. Implement schema version checks and explicit migration commands.
5. Implement store methods and transactional semantics.
6. Add concurrency tests for idempotency, state transitions, claims, and locks.
7. Test clean install, repeated status, upgrade, backup, and restore.
8. Destroy only the disposable test database after evidence is captured.

## Automated Acceptance

- Clean migration applies on every supported PostgreSQL major selected for v1.
- Migration status is deterministic and startup detects old/new schema mismatch.
- Concurrent identical submissions yield one task ID.
- Concurrent conflicting submissions yield one success and conflicts.
- Concurrent claim attempts claim each row once.
- Only one advisory-lock session succeeds.
- Lease/heartbeat/retention queries satisfy integration tests.
- Runtime role cannot alter schema.
- Go race, vet, build, tests, and whitespace checks pass.

## Manual Acceptance

- Migration SQL is understandable without an ORM.
- No table or column names a concrete application, model, or runtime.
- Test database coordinates are disposable and absent from Git.
- Backup and restore procedure protects prompts/results as sensitive data.

## Stop Conditions

- The selected database is not clearly disposable and dedicated.
- A migration command could target an unrelated database without confirmation.
- Correctness depends only on in-process mutexes.
- Retention deletion breaks idempotency semantics.
- Rollback/restore behavior is unknown for a destructive migration.

## Handoff

### Changed Files

- `migrations/embed.go` and `migrations/00001_initial_schema.sql`: embedded
  Goose migrations (8 tables, CHECK constraints, partial indexes).
- `internal/store/postgres/`: `store.go` (pool/options), `tasks.go`
  (create/idempotent replay/conflict, transitions, claim with
  `FOR UPDATE SKIP LOCKED`, lease renew), `clients.go`, `lock.go` (session
  advisory lock), `heartbeat.go`, `cleanup.go` (bounded retention purges),
  `schema.go` (version compatibility check).
- Integration suite: `harness_test.go` (TestMain reset/migrate/roles),
  `schema_test.go`, `tasks_test.go` (incl. concurrency), `clients_test.go`,
  `lock_test.go`, `heartbeat_test.go`, `cleanup_test.go`.
- `cmd/llm-broker/migrate.go` + tests: `migrate status|up` with config and
  environment URL resolution.
- `scripts/pg-test-up.sh` / `scripts/pg-test-down.sh`: disposable test
  database lifecycle (loopback, port 55432).
- `docs/database.md`: schema, roles, migrations, retention, backup/restore.
- `go.mod`/`go.sum`: goose v3.27.3, pgx v5.10.0, google/uuid.

### Validation Evidence

- `go test ./...`: all packages pass (integration suite against disposable
  PostgreSQL 17 on 127.0.0.1:55432).
- `go test -race ./...`: store integration suite passes.
- `go vet ./...`, `gofmt -l .` (empty), `go build ./...`: clean.
- Concurrency tests: 16 identical concurrent submissions produce exactly one
  task and 15 replays; 10 mixed submissions produce 1 created / 4 replays /
  5 conflicts; 12 concurrent claimers claim each of 5 tasks exactly once;
  exactly one advisory-lock session holds.
- Runtime role DML works; DDL fails with permission denied.
- `migrate status` and `migrate up` smoke-tested against the disposable
  database; `status` reports applied migrations and version 1.
- Cleanup: payload (1h), result (24h), metadata (30d) purged in order;
  idempotency key + hash survive payload purge and replay to the original
  task.

### Manual Acceptance

- Review `migrations/00001_initial_schema.sql` directly (no ORM).
- `scripts/pg-test-up.sh` then `scripts/pg-test-down.sh` destroy the test
  database; no existing database is touched.
- Test database coordinates are generated at runtime and absent from Git.

### Compatibility Questions

- The claim primitive is intentionally policy-free (oldest first) so it is a
  reusable primitive; scheduler policy (priority, aging, affinity) composes
  above it in P05.
- `task_results` is written by the worker phase (P07); the cleanup suite
  inserts fixture rows to exercise purge until then.
- `migrate` has no `down` command in v1: forward-only with documented
  irreversible boundaries, per the phase spec.

### Suggested Commit Message

`feat: P03 PostgreSQL persistence (migrations, store, migrate commands, integration tests)`

Stop for approval before P04.
