# Integration Phase L04: Stabilization

## Status

- Phase: L04.
- State: `pending`.
- Prerequisite: accepted bounded result from L03 and explicit soak approval.
- Runtime impact: controlled traffic expansion within approved limits.

## Objective

Prove sustained safe operation, recovery, retention, and workload behavior before
deciding whether legacy direct-runtime capabilities may be removed.

## In Scope

- Gradual approved traffic expansion and soak monitoring.
- Queue/aging/affinity/workload policy tuning within documented config bounds.
- Backup/restore, service restart, worker standby/lock, database/backend outage,
  and indeterminate recovery drills during controlled windows.
- Retention verification on real operational metadata.
- Operator runbook/on-call documentation.
- Decision package for retaining or separately removing legacy path/restart access.

## Out Of Scope

- Automatic removal of legacy path, service files, permissions, or code.
- Product API/schema changes.
- Parallel inference/resource pools.
- Thinking model introduction.
- Unbounded traffic expansion.

## Preconditions

- No unresolved L03 discrepancy or indeterminate task.
- Current backups and tested rollback remain available.
- Soak duration, workload bounds, monitors, owner, and abort thresholds approved.
- Privacy and public-listener checks pass immediately before expansion.
- Task/domain reconciliation is automated or operationally reliable.

## Stabilization Requirements

- One inference globally remains continuously observed.
- Queue age and starvation metrics validate policy under actual workload mix.
- Affinity does not delay higher effective priority beyond policy.
- Retention deletes payload/result/metadata at approved boundaries.
- Service/host restart recovers queued tasks without duplicate execution.
- Indeterminate drill fails closed and follows accepted fencing/recovery runbook.
- Client cannot restart/change runtime or select backend/model/priority.
- Direct backend access remains restricted.

## Procedure

1. Establish baseline dashboards and reconciliation report.
2. Expand one bounded workload/time segment at a time.
3. Review errors, queue age, output validation, resources, and privacy at each gate.
4. Run approved restart/outage/backup/restore/recovery drills.
5. Verify retention over actual elapsed boundaries or controlled test rows.
6. Tune only operator policy values within the accepted product contract.
7. Complete operator troubleshooting and recovery runbooks.
8. Prepare a separate evidence-based legacy-removal proposal; do not execute it.

## Automated Acceptance

- Soak meets approved availability/error/latency/queue thresholds.
- No overlapping inference or duplicate domain mutation is observed.
- Aging/batch tests match production metrics/replays.
- Restart/outage/recovery drills preserve correctness.
- Retention and backup/restore checks pass.
- Secret/prompt/result/reasoning scans remain clean.
- API/runtime listener and authorization checks remain private and least privilege.

## Manual Acceptance

- Every work item in sampled reconciliation is accounted for.
- Operators can execute recovery without source-code intervention.
- Domain validation quality is at least the accepted pre-cutover baseline.
- Reviewer decides whether to keep rollback indefinitely or approve a new,
  separately specified legacy-removal phase.

## Stop Conditions

- Duplicate inference/domain effect, lost work, privacy leak, or backend bypass.
- Queue starvation or resource pressure exceeds threshold.
- Recovery requires guessing whether a generation is still active.
- Retention or backup behavior risks sensitive data/correctness.
- Proposed tuning changes public semantics instead of operator config.

## Handoff

Include soak window/volume, reconciliation, SLO/queue/resource metrics, drill and
retention evidence, incidents, current rollback state, runbook links, both Git
statuses, and legacy-retention/removal recommendation. Do not remove legacy assets
without a new explicit phase and approval.
