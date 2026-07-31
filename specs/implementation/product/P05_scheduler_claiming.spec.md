# Product Phase P05: Scheduler And Claiming

## Status

- Phase: P05.
- State: `accepted`.
- Prerequisite: accepted P04.
- Runtime impact: isolated PostgreSQL tests only; no backend inference.

## Objective

Implement deterministic broker-owned scheduling, starvation prevention, bounded
backend affinity, global worker leadership, atomic claims, and leases.

## In Scope

- Priority classes and workload-to-priority resolution.
- Aging, FIFO ties, eligibility, retry availability, and cancellation exclusion.
- Backend/model affinity batch state.
- Global advisory-lock leadership.
- Atomic queued-to-running claims and task leases.
- Active/standby heartbeat state.
- Pure and PostgreSQL concurrency/property tests.

## Out Of Scope

- Backend adapter implementation or inference calls.
- Retry execution, response validation, or result persistence.
- Real runtime/controller integration.
- Parallel inference or configurable resource pools.

## Deliverables

- `internal/scheduler` policy/selection implementation.
- Store queries for eligible selection, claim, lock, lease, and heartbeat.
- Worker leadership component without execution loop.
- Scheduler decision diagnostics with sanitized bounded fields.
- Deterministic clock/randomness test seams.

## Policy Requirements

- Base priorities: high 300, normal 200, batch 100.
- Aging adds 100 per 10 queued minutes and caps at 300.
- Ties use creation time then task ID.
- Clients never submit priority or batch controls.
- Only queued, available, non-cancelled, granted tasks are eligible.
- Affinity batch defaults: four tasks or ten minutes.
- Any queued task with higher effective priority closes the batch before the next
  claim, never during the active task.
- A batch is a preference, not an entitlement; aging still applies.
- Several configured backends never permit simultaneous claims in v1.

## Exclusion Requirements

- Active worker owns a dedicated session advisory lock connection.
- Standby workers never claim.
- Claim and attempt/lease creation occur in one transaction.
- `SKIP LOCKED` handles contention but does not define global concurrency.
- Heartbeat identifies active/standby and lock ownership accurately.
- Lock loss stops new claims immediately and makes worker unready.

## Procedure

1. Implement pure effective-priority and tie-breaking functions.
2. Implement explicit SQL ordering with a database timestamp snapshot.
3. Add affinity batch open/close rules around selection.
4. Implement leadership connection and advisory lock lifecycle.
5. Implement atomic claim/attempt/lease transaction.
6. Implement heartbeat and lease-refresh primitives.
7. Add two-process contention, lock-loss, clock-boundary, and starvation tests.
8. Verify no code path claims without current leadership.

## Automated Acceptance

- Table/property tests cover priorities, aging boundaries, ties, and batches.
- A continuously backlogged batch workload cannot starve older work.
- Higher effective priority closes affinity after the current claim.
- Concurrent workers produce one leader and no standby claim.
- Forced lock-connection loss prevents subsequent claims.
- Concurrent selectors do not duplicate claims.
- Global claim test remains serial across different backend names.
- Race, vet, build, database, and whitespace checks pass.

## Manual Acceptance

- Reviewer can derive the next task from fixture data without hidden heuristics.
- Diagnostics explain class/aging/affinity without prompt/client/task labels in
  metrics.
- Global-one invariant is obvious in code and tests.

## Stop Conditions

- Correctness depends on process-local locking.
- A standby can claim during lock churn.
- Multiple backend names produce parallel running claims.
- Scheduler SQL and pure policy disagree.
- Batch affinity can override higher effective priority.

## Handoff

### Changed Files

- `internal/scheduler/`: broker-owned policy and selection — `policy.go`
  (Policy defaults: 60s lease, 4-task/10-minute batches, 10s heartbeat;
  EffectivePriority delegates to the domain function), `batch.go`
  (AffinityState + sanitized Decision diagnostics), `select.go` (eligible
  selection SQL generated from the same domain constants as the pure
  policy), `leadership.go` (Leader: session advisory lock on a dedicated
  connection, active/stopped heartbeats, lock loss stops claims),
  `scheduler.go` (single ClaimNext path, mutex-guarded, refuses without
  leadership).
- `internal/store/postgres/tasks.go`: extracted the single claim
  transaction into `claimSelect`; added `ClaimNextByPolicy` (scheduler-owned
  query, claims with grants/attempts eligibility) and `ClaimedTask` with the
  SQL-computed `EffectivePriority`; `ClaimNextQueued` (FIFO) kept as the
  policy-free store baseline for tests.
- Tests: `internal/scheduler/policy_test.go` (pure boundaries),
  `batch_test.go` (state machine), `harness_test.go` (integration harness on
  the disposable DB), `leadership_test.go` (one leader, standby never
  claims, lock loss, reacquire, heartbeat lifecycle),
  `scheduler_test.go` (class order, aging caps, tie-break, eligibility,
  concurrency, serial across capabilities, affinity, SQL-vs-domain
  conformance matrix).
- `docs/scheduler.md`: policy, ordering, batches, leadership, single claim
  path.
- `.github/workflows/ci.yaml` and `docs/database.md`: integration test
  binaries must run serially (`go test -p 1 ./...`) because the package
  harnesses share one disposable database and reset its schema.
- `specs/.../STATUS.md` and this spec: state updated.

### Validation Evidence

- `go test -p 1 ./... -count=1` and `go test -race -p 1 ./... -count=1`:
  all packages pass (three integration harnesses coexist serially).
- `go vet ./...`, `go build ./...`, `gofmt -l .` (empty), `git diff --check`
  (clean).
- Leadership: two leaders contend; exactly one acquires; standby claims are
  refused with `ErrNotLeader`; forced release stops claims immediately and
  allows reacquire; heartbeat transitions active → stopped.
- Concurrency: six concurrent claimers over five tasks claim each task
  exactly once; claims stay serial across different capabilities.
- Conformance: SQL-computed effective priority equals the domain function
  for classes high/normal/batch across ages 0/600/1800/3600/7200 seconds
  (including per-class caps: batch reaches 400, normal caps at 300, high
  never ages).
- Aging: an aged batch task (400) outranks a fresh high (300); a fresh
  normal (200) comes last in that fixture.

### Compatibility Questions

- `go test ./...` without `-p 1` is flaky by design: the shared test DB is
  reset per package harness, so package binaries must run serially. CI and
  docs encode `-p 1`.
- The eligibility query requires a current workload grant at claim time: a
  revoked grant stalls the queued tasks of that client until re-granted
  (intended: grants gate admission).
- The API `/ready` still checks only database/schema state; worker
  heartbeat/lock readiness joins in P07 (worker execution phase).
- `Decision` diagnostics carry capability slugs and priorities only; they
  are log-bound and will feed sanitized metrics in P08.

### Suggested Commit Message

`feat: P05 scheduler and claiming (policy ordering, aging, affinity batches, leadership, single claim path)`

Stop for approval before P06.
