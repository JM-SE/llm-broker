# Integration Phase L03: Controlled Cutover

## Status

- Phase: L03.
- State: `pending`.
- Prerequisite: accepted L02 plus explicit cutover approval.
- Runtime impact: first phase allowed to route real YTPipe work through broker.

## Objective

Activate the approved broker/runtime deployment and route a bounded YTPipe canary
through it without losing, duplicating, or bypassing durable work.

## In Scope

- Fresh baseline, backups, reconciliation, and rollback verification.
- Dedicated production broker PostgreSQL, API/worker, llama-swap, and model
  services using private values outside Git.
- Broker/YTPipe credentials and workload grant provisioning.
- Deploy YTPipe adapter disabled, then enable for a bounded canary.
- Disable YTPipe model-service restart authority in broker mode.
- Observe queue, task/result reconciliation, one-inference, and domain outcomes.
- Roll back on predefined triggers.

## Out Of Scope

- Immediate all-workload migration.
- Removing legacy code/unit/files or rollback capability.
- Adding Thinking model/capability.
- Allowing any client direct access to llama-swap/model runtime.
- Changing deterministic YTPipe domain rules.

## Preconditions

- L02 restoration and evidence accepted.
- Exact deployment configs/secrets/ports/paths reviewed privately.
- Database and relevant YTPipe state backed up and restore-tested.
- No unresolved indeterminate task or runtime generation.
- Canary scope, task count/time, owner, monitors, and rollback thresholds approved.
- Client work queue is reconciled; every canary operation has stable idempotency.
- Legacy path remains available for rollback but cannot run concurrently against
  the same logical operation.

## Cutover Requirements

- Broker API and llama-swap are private; only intended OS/network principals can
  access them.
- llama-swap privacy/global-concurrency settings match accepted L02.
- API and worker readiness pass before enabling client traffic.
- YTPipe direct restart/model control is disabled before broker mode is enabled.
- Canary tasks are identified by non-sensitive correlation metadata.
- Every YTPipe operation reconciles to one broker task and one domain result.
- No deterministic filter or business rule is delegated to model output.

## Rollback Triggers

- Duplicate/overlapping inference or duplicate domain mutation.
- Lost/unreconcilable task or result.
- Broker/runtime privacy leak or public listener exposure.
- Unresolved indeterminate execution.
- Error/latency/resource threshold exceeded.
- YTPipe attempts direct backend restart or bypass.
- Database/readiness/recovery behavior differs materially from L02.

## Procedure

1. Capture fresh baseline and confirm rollback commands.
2. Deploy/migrate dedicated broker database and services without traffic.
3. Provision least-privilege backend/client credentials and grants.
4. Verify listeners, auth, schema, readiness, leadership, privacy, and exclusion.
5. Deploy YTPipe adapter with feature disabled and run smoke checks.
6. Disable direct runtime restart authority and enable only approved canary scope.
7. Reconcile each task/result/domain effect while monitoring thresholds.
8. Disable canary at its bound even if healthy.
9. Either roll back on trigger or retain bounded deployment pending review; do not
   expand traffic in this phase.

## Automated Acceptance

- Pre-traffic security/readiness checks pass.
- Canary reconciliation proves one YTPipe operation to one broker task/result.
- Maximum active inference remains one.
- Idempotent retries create no duplicate task/domain effect.
- Logs/metrics leak scan passes with real workload canaries redacted.
- Backup/rollback commands remain executable and health checks pass.

## Manual Acceptance

- Operator accounts for every preexisting and canary work item.
- Canary outputs pass existing YTPipe deterministic validation.
- No direct runtime request/restart from YTPipe is observed.
- Reviewer decides rollback, continued bounded operation, or L04 entry.

## Stop Conditions

- Any precondition is stale or unverified.
- Real work cannot be bounded/reconciled before enablement.
- Canary requires changing product contracts/specs during the window.
- Any rollback trigger occurs.
- Expansion beyond approved scope is requested without a new gate.

## Handoff

Include exact canary scope/count, reconciliation table without sensitive content,
latency/error/resource metrics, listener/auth/restart evidence, current deployment
state, rollback availability, both Git statuses, and recommendation. Stop for L04
approval or execute rollback.
