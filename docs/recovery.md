# Worker And Recovery Runbook

This document explains how the execution worker behaves, what can go wrong,
and what the operator must do when an outcome is ambiguous. It complements
`docs/scheduler.md` (selection) and `docs/database.md` (schema/retention).

## Architecture

- `llm-broker worker` runs the single active executor. It acquires the global
  execution lock (session advisory lock) and the active worker heartbeat; any
  second worker process stands by and never claims tasks.
- The API and the worker use independent database pools. The worker is the
  only component that calls inference adapters.
- Exactly one adapter call is in flight globally. The worker is strictly
  serial: it claims one task, executes it end to end, persists the terminal
  outcome, and only then claims the next.

## Task lifecycle inside the worker

1. Claim: the scheduler selects the best eligible task (priority, aging,
   affinity batch) and flips it to `running` with a fresh attempt and a
   lease, in one transaction.
2. Policy: the workload policy supplies the attempt budget, retry delays,
   output bound, required features, and the per-attempt timeout.
3. Cancel check: if the API recorded cancel intent before the call, the task
   becomes `cancelled` with no inference.
4. Call: the adapter is invoked under the workload timeout; the lease is
   renewed continuously while the call runs.
5. Outcome:
   - Success: output is size-checked and validated against the response
     contract (text / valid JSON / JSON Schema). A cancellation requested
     during the call discards the output and cancels the task. Otherwise the
     result row and the `succeeded` transition are persisted atomically.
   - Safe transient (`transient_unsent`, `transient_safe`): the task is
     requeued with a persisted availability time (bounded backoff) and the
     attempt is recorded as failed. If the attempt budget is exhausted, the
     task fails terminally.
   - Terminal failure (`backend_rejected`, `output_invalid`, ...): the task
     fails immediately, no retry.
   - Indeterminate: see next section.

## Ambiguous outcomes (indeterminate)

An indeterminate outcome means the backend may or may not have executed the
request (connection dropped mid-response, timeout during the call, unknown
error). The worker's rule is conservative:

- The attempt is recorded as failed with `error_class=indeterminate` and a
  stable code.
- The task stays `running` until the operator resolves it (its lease is not
  renewed and expires).
- **All new inference stops**: the worker refuses to claim anything while any
  unresolved ambiguous execution exists. The API readiness endpoint reports
  503 (`an ambiguous execution requires operator action`).

### Operator procedure

1. Inspect:

   ```
   llm-broker worker status
   ```

   Lists every unresolved task with its attempt, error class/code, and
   creation time.

2. Establish the backend state with the runtime's own evidence (for example
   check whether the model server is idle, or fence it). This evidence is the
   authorization for the decision; it is not recorded automatically.

3. Decide and resolve:

   - The backend provably never ran the request, or is provably idle and the
     task is safe to redo:

     ```
     llm-broker worker resolve <task-id> --outcome=requeue [--delay=N]
     ```

     The task returns to `queued` and becomes eligible at now+N seconds
     (default immediately). The attempt records
     `error_class=indeterminate, error_code=operator_requeue`.

   - The outcome cannot be trusted; terminate the task:

     ```
     llm-broker worker resolve <task-id> --outcome=failed
     ```

     The task fails with `error_code=operator_failed`. Clients see the
     classified error and can resubmit with a fresh idempotency key.

4. The worker unblocks automatically once no unresolved ambiguous execution
   remains. It never auto-retries an indeterminate task and never duplicates
   an adapter call based on an expired lease alone.

## Graceful shutdown

`SIGTERM`/`SIGINT` stops new claims immediately. The in-flight inference is
not preempted: it continues under its workload deadline and its terminal
outcome is persisted (the worker's database operations run on a context that
survives the shutdown signal). If the drain bound is exceeded, the process
exits and the task remains `running`; after its lease expires it appears in
`worker status` if it was indeterminate, or is simply stale — a restart
never authorizes a duplicate call by itself.

## Readiness semantics

`GET /ready` (API) reports 200 only when: the database is reachable and
migrated, at least one worker heartbeat is fresh (an active worker holds the
execution lock), and no unresolved ambiguous execution exists. Otherwise 503
with a stable code.

## Operational notes

- Never run two `llm-broker worker` processes expecting parallelism: the
  second one stands by by design.
- After a worker crash, inspect `worker status` before restarting the worker
  if any task was mid-flight.
- Backoff is persisted on the task row (`retry_available_at`), so a restart
  preserves retry timing.
- Logs are sanitized: task IDs, classes, and codes, never prompts, results,
  or credentials.
