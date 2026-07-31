# Product Phase P05: Scheduler And Claiming

## Status

- Phase: P05.
- State: `pending`.
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

Include policy fixture outputs, concurrency/leadership evidence, SQL plan notes,
changed files, Git status, and a suggested commit message. Stop for approval
before P06.
