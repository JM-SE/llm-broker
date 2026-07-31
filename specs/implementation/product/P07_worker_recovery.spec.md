# Product Phase P07: Worker And Recovery

## Status

- Phase: P07.
- State: `pending`.
- Prerequisite: accepted P06.
- Runtime impact: isolated PostgreSQL and fake/test HTTP backends only.

## Objective

Complete durable end-to-end serial execution with retries, result validation,
cancellation, leases, graceful shutdown, and fail-closed ambiguous recovery.

## In Scope

- Worker command and bounded poll loop.
- Active leadership, scheduling, claim, adapter call, and terminal persistence.
- Attempt budgets and backoff.
- Structured-output validation.
- Running cancellation semantics.
- Lease/heartbeat refresh and shutdown drain.
- Startup/stale-task/indeterminate recovery workflow.
- API/worker end-to-end tests with fake and test HTTP backend.

## Out Of Scope

- Real model/runtime calls.
- Provider-specific runtime controller.
- Production service/container deployment.
- Parallel workers executing inference.

## Deliverables

- `internal/worker` orchestration and tests.
- Worker readiness/heartbeat integration.
- Retriable/terminal/indeterminate transition implementation.
- Operator CLI/status path for unresolved recovery.
- End-to-end scenario and fault-injection suite.
- Recovery runbook independent of runtime vendor.

## Execution Requirements

- Worker verifies lock ownership before every claim and adapter call.
- Exactly one adapter `Infer` call may be active globally.
- API and worker use independent database pools.
- Every task attempt has overall/workload deadline and heartbeat.
- Adapter output is size-limited and response-contract validated before success.
- Raw provider response is never stored.
- Cancellation observed before call produces cancelled without inference.
- Cancellation during call never preempts; returned output is discarded and task
  becomes cancelled after the call finishes.

## Retry And Recovery Requirements

- Only definitely-unsent and proven-safe transient classes retry automatically.
- Maximum attempts includes first call; default is three.
- Backoff is bounded, persisted via availability time, and restart-safe.
- Indeterminate attempt blocks the worker from all new inference.
- Expired lease creates recovery work but never authorizes a duplicate call.
- `none` controller requires operator evidence/fencing before recovery resolution.
- Recovery actions are auditable sanitized metadata and non-client CLI only.

## Procedure

1. Implement the worker lifecycle and explicit active/standby states.
2. Connect scheduler claim to adapter resolution and invocation.
3. Implement lease heartbeat independent enough to survive normal long calls.
4. Implement output validation and terminal transactions.
5. Implement persisted backoff and safe retry selection.
6. Implement cancellation before/during execution.
7. Implement stale-running and indeterminate startup recovery gates.
8. Implement graceful shutdown with bounded drain and no new claims.
9. Add crash/disconnect/database-loss/lock-loss fault tests.

## Automated Acceptance

- Fake end-to-end covers success, failure, retry, cancellation, and wait result.
- Structured invalid output fails without persisting raw/reasoning data.
- Two workers never overlap adapter calls.
- Restart preserves queued/retry tasks and task IDs.
- Crash before send can retry safely; crash/disconnect after possible send blocks.
- Expired lease never triggers duplicate inference automatically.
- Lock/database loss stops claims and readiness.
- Graceful shutdown drains or records recovery state correctly.
- Race, vet, build, database, fault, and whitespace checks pass.

## Manual Acceptance

- Operator runbook can identify and resolve an indeterminate fake scenario.
- API process remains unable to invoke an adapter.
- Task/attempt records explain outcomes without sensitive body content.
- Exactly-one evidence spans process, database, and adapter-call counters.

## Stop Conditions

- Any retry decision relies on generic HTTP failure alone.
- Worker can become ready with unresolved indeterminate execution.
- Cancellation kills a backend call as its correctness mechanism.
- Duplicate adapter invocation appears in contention/crash tests.
- Output or reasoning leaks into operational records.

## Handoff

Include scenario matrix, overlapping-call evidence, crash/recovery transcript,
readiness state, changed files, Git status, and a suggested commit message. Stop
for approval before P08.
