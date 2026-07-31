# Integration Phase L01: YTPipe Adapter Offline

## Status

- Phase: L01.
- State: `pending`.
- Prerequisite: accepted L00.
- Runtime impact: none; adapter remains disabled and undeployed.

## Objective

Prepare YTPipe to use the durable broker task API while preserving its existing
active behavior and preventing any accidental broker traffic.

## In Scope

- Read current YTPipe source/tests/config immediately before editing.
- Introduce a broker client/adapter behind an explicit disabled feature flag.
- Map prompts/messages, workload, capability, idempotency, queue polling/wait,
  result, cancellation, and broker error classes.
- Remove restart/model authority from the broker-enabled path.
- Add unit/integration tests using fake HTTP server only.
- Document deployment/cutover/rollback configuration without enabling it.

## Out Of Scope

- Deploying/restarting YTPipe.
- Changing its active environment or systemd unit.
- Contacting the real broker, llama-swap, llama-server, or model.
- Removing the current direct adapter before canary acceptance.
- Processing or mutating production jobs.
- Changing YTPipe domain filters or allowing model output to override them.

## Deliverables

- YTPipe broker adapter with explicit disabled default.
- Typed mapping for broker task/error/result states.
- Stable idempotency-key derivation from durable YTPipe operation identity.
- Tests for submit replay, wait/poll, timeouts, queue delay, auth, admission,
  broker unavailable, model failure, invalid output, and cancellation.
- Disabled rollout configuration and rollback guide.
- Cross-repository change/evidence report.

## Adapter Requirements

- Use workload/capability only; never send backend/model/URL/priority.
- Persist/reuse an operation's idempotency key across process retries.
- Prefer bounded `Prefer: wait=N` where configured, then poll durable task state.
- Distinguish submission/queue/broker/backend/output failures for retry/reporting.
- Treat HTTP client timeout/disconnect as unknown submission state and reconcile by
  repeating the same idempotency key.
- Never restart or reconfigure a shared model service on broker errors.
- Never log token, prompt, CV/domain payload, full output, or idempotency key.
- Preserve existing deterministic domain validation and filtering.

## Safety Requirements

- Feature flag defaults to disabled when unset/invalid.
- Broker base URL/token are not required while disabled.
- No import-time/startup network call occurs while disabled.
- Tests assert zero broker requests in disabled mode.
- Existing direct behavior and tests remain unchanged while disabled.
- No service/env/deployment file used by the active process is modified unless it
  is an explicitly dormant example.

## Procedure

1. Confirm active YTPipe Git/service/job state and identify unrelated changes.
2. Add adapter boundary with disabled default and zero side effects.
3. Implement broker task lifecycle and error mapping.
4. Disable restart/model-control behavior only inside broker-enabled path.
5. Add fake-server tests including response loss and idempotent reconciliation.
6. Run complete YTPipe suite in disabled mode and adapter-specific tests.
7. Inspect logs/config diffs for sensitive values and accidental enablement.
8. Confirm deployed files/process/environment are unchanged.

## Automated Acceptance

- Existing YTPipe suite passes with broker disabled.
- Disabled mode generates zero network request to broker fixtures.
- Adapter tests cover all mapped task/error states.
- Same operation retries with same idempotency key and no duplicate logical work.
- Timeout after submit reconciles through idempotent replay.
- Broker mode never calls restart/service/model-control code.
- Log leak tests pass.
- No real network/model is required.

## Manual Acceptance

- Source diff is separable from deployment/cutover.
- Active YTPipe service/process/config and queued work are unchanged.
- Rollback is simply leaving/disabling the feature flag before deployment.
- Reviewer approves domain error semantics and idempotency identity.

## Stop Conditions

- YTPipe has unrelated conflicting edits in files that must change.
- Idempotency cannot be tied to durable operation identity.
- Disabled mode changes active behavior or requires broker availability.
- Tests would use production prompts/data or real runtime.
- Editing requires deployment/restart in the same phase.

## Handoff

Include both repositories' Git status, changed files, test results, disabled-mode
zero-request evidence, active-service no-change evidence, rollback config, and a
suggested commit message. Stop for approval before L02.
