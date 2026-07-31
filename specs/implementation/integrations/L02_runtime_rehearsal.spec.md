# Integration Phase L02: Real Runtime Rehearsal And Restoration

## Status

- Phase: L02.
- State: `pending`.
- Prerequisite: accepted L01 plus separate maintenance-window approval.
- Runtime impact: temporary real-runtime changes followed by mandatory restoration.

## Objective

Prove the broker, dedicated PostgreSQL, llama-swap, and selected Qwen model work
together with synthetic data, then restore the pre-rehearsal runtime exactly.

## In Scope

- Capture a complete current-state and rollback baseline.
- Create dedicated broker database/container/credentials on loopback/private
  interfaces according to approved private configuration.
- Install/start dormant broker and llama-swap rehearsal processes/services.
- Temporarily stop or isolate the legacy direct model server only during the
  approved window.
- Run synthetic functional, serialization, model-switch, timeout, disconnect,
  idleness/fencing, recovery, privacy, retention, and resource tests.
- Restore legacy runtime/listener/service state before phase completion.
- Keep YTPipe broker integration disabled and undeployed.

## Out Of Scope

- Routing any real YTPipe or user workload.
- Using production prompts, CVs, results, or idempotency keys.
- Permanent cutover or removal of legacy service.
- Enabling Thinking model/capability.
- Leaving an indeterminate generation or modified listener after rehearsal.

## Preconditions

- Approved exact window, owner, stop time, and abort authority.
- No active or queued work depends on the legacy runtime; state is reconciled.
- Current service files, process arguments, listeners, hashes, enablement state,
  logs baseline, and rollback commands are captured securely outside Git.
- Required binaries/models already exist or their installation is separately
  approved and checksum-verified.
- Dedicated broker database backup/removal plan exists.
- Synthetic test payloads are reviewed as non-sensitive.

## Rehearsal Requirements

- llama-swap uses `captureBuffer: 0`, `sendLoadingState: false`, broker-only key,
  private bind, `concurrencyLimit: 1`, and one-model behavior without `matrix`.
- Broker is the only rehearsal caller of llama-swap.
- Broker API is private/loopback and client credentials are temporary.
- Qwen model runs with approved context and one parallel inference.
- Concurrent synthetic submissions demonstrate one active inference.
- Disconnect after possible submission exercises indeterminate fail-closed state.
- Runtime-controller idleness/fence evidence is tested if L00 provided one;
  otherwise operator recovery remains manual and explicit.
- Logs/metrics/database are scanned for reasoning and secret canaries.

## Mandatory Restoration

- Stop rehearsal broker/worker/llama-swap processes.
- Restore prior model-server unit/script/content, enablement, arguments, listener,
  and active state from captured evidence.
- Verify legacy health and one synthetic direct request if approved.
- Verify YTPipe remains on its pre-rehearsal configuration and can use the legacy
  path if it was healthy before the window.
- Preserve rehearsal database/log evidence securely or destroy it per plan.
- Restoration is required even when all tests pass.

## Procedure

1. Execute preflight and capture baseline/evidence.
2. Reconfirm no active work and announce maintenance start.
3. Start dedicated PostgreSQL and migrate/provision temporary credentials.
4. Start llama-swap/Qwen with sanitized-profile-derived private config.
5. Start broker API/worker and verify auth/readiness/leadership.
6. Run the approved synthetic test matrix and monitor resources.
7. Abort immediately on duplicate inference, leakage, unresolved runtime activity,
   or restoration risk.
8. Perform mandatory restoration and compare against baseline.
9. Record discrepancies and keep cutover blocked.

## Automated Acceptance

- Synthetic API/idempotency/scheduler/result/cancel tests pass.
- Measured maximum simultaneous backend inference is one.
- Backend/model health and configured generation limits pass.
- Indeterminate test blocks new tasks until proven recovery.
- Secret/prompt/reasoning canary scan passes.
- Retention and restart tests preserve expected state.
- Post-restoration listener/process/unit/hash comparison matches baseline or has an
  explicitly reviewed benign difference.

## Manual Acceptance

- Operator confirms no production work was submitted or lost.
- Legacy runtime is restored and healthy before closing window.
- YTPipe remains disabled for broker mode and operational as before.
- Resource/latency evidence is acceptable for a future canary.
- Reviewer approves the exact L03 rollback trigger/commands.

## Stop Conditions

- Active work cannot be reconciled before stopping legacy runtime.
- Required baseline/rollback artifact is missing.
- More than one inference is observed.
- llama-swap capture/privacy/exclusion settings are not effective.
- Backend activity remains ambiguous and cannot be fenced.
- Restoration cannot reproduce the prior healthy state.

## Handoff

Include window timeline, baseline/restoration comparison, test matrix, resource
metrics, failure/recovery evidence, leak scan, service/listener states, database
state, YTPipe unchanged evidence, Git status, and cutover recommendation. Stop for
separate L03 approval.
