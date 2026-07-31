# Scheduler

The scheduler is the broker-owned decision layer. Clients never influence
ordering: every decision derives from operator configuration (workload
policies) and durable task state. This document explains the policy, the
affinity batch, and global leadership.

## Effective priority

Each queued task is scored once per selection with a database timestamp
snapshot:

```
effective = class_base + min(aging_boost, class_cap)
```

- Class bases: `high` 300, `normal` 200, `batch` 100.
- Aging boost: `+100` per 10 queued minutes.
- Per-class caps (aged work never overtakes an operator-assigned class):
  - `high` never ages (cap 0).
  - `normal` caps at 100 (can reach, never exceed, the high base of 300).
  - `batch` caps at 300 (can reach 400, the top of normal territory).

The formula lives in one place — `internal/domain` — and the SQL ordering
expression is generated from the same constants (`internal/scheduler`). A
conformance test claims fixtures across all classes and ages and compares
each SQL-computed priority against the pure domain function.

## Ordering and ties

1. Highest effective priority first.
2. Then oldest `created_at`.
3. Then task ID (deterministic total order).

Eligibility is enforced in the same query: task is `queued`, attempts are
below the workload retry budget, and the client still holds a workload
grant.

## Affinity batches

The affinity batch prefers consecutive claims of the same capability
(backend/model affinity). It is a preference, not an entitlement:

- A batch opens with the first claim and continues while the next best
  eligible task has the same capability.
- A batch closes when it reaches 4 claims or 10 minutes, or when the best
  eligible task has a different capability — a higher effective priority
  task always closes the batch before the next claim, never during the
  running inference.
- Aging keeps applying inside a batch; the global ordering is never
  bypassed.

## Global leadership

Exactly one worker is active in v1. The active worker holds a PostgreSQL
session advisory lock on a dedicated connection and refreshes a heartbeat
while active:

- Standby workers (no lock) never claim; `ClaimNext` refuses with
  `ErrNotLeader`.
- Claim, attempt record, and lease are created in one transaction with
  `FOR UPDATE SKIP LOCKED` (each task claimed exactly once, even under
  contention).
- Graceful release announces a `stopped` heartbeat; a failed heartbeat
  treats leadership as lost and stops new claims immediately.

## Single claim path

All claims flow through `Scheduler.ClaimNext` → `Store.ClaimNextByPolicy` →
one shared claim transaction. The policy-free FIFO primitive
(`ClaimNextQueued`) remains only as the store-level baseline used by tests;
product code has exactly one claim path.
