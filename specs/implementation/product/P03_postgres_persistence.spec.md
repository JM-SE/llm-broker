# Product Phase P03: PostgreSQL Persistence

## Status

- Phase: P03.
- State: `pending`.
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

Include PostgreSQL versions, migration hashes/status, concurrency test evidence,
role permissions, cleanup evidence, changed files, Git status, and a suggested
commit message. Stop for approval before P04.
