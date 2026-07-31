# Product Phase P08: Security, Privacy, And Observability

## Status

- Phase: P08.
- State: `pending`.
- Prerequisite: accepted P07.
- Runtime impact: isolated test deployment only.

## Objective

Harden the complete generic product for hostile input, sensitive prompts/results,
bounded operation, diagnosis, retention, and backup/restore.

## In Scope

- Structured sanitized logging.
- Prometheus metrics with bounded labels.
- Full health/readiness semantics.
- Payload/result/metadata retention jobs.
- HTTP/database/backend resource limits.
- Authentication/admission abuse controls.
- Security, privacy, recovery, and dependency threat review.
- Backup/restore and upgrade rehearsal on disposable data.
- Fault, load, fuzz, leak, and vulnerability tests.

## Out Of Scope

- Packaging/release artifacts.
- Real backend/runtime or client integration.
- External observability stack deployment.
- TLS certificate automation or internet exposure.

## Deliverables

- `internal/metrics` definitions/instrumentation.
- Stable structured event catalog and redaction tests.
- Bounded retention maintenance implementation.
- Updated security/privacy/operator documentation.
- Threat model and supported deployment assumptions.
- Fault/load/leak test reports or reproducible commands.

## Observability Requirements

- Metrics cover API, auth, queue, age, scheduling, inference, attempts, failure
  class, leadership, heartbeat, readiness, and retention.
- Labels exclude client/task/key/model/URL/raw errors and arbitrary input.
- Logs exclude credentials, prompts, outputs, schemas, raw bodies, and reasoning.
- Stable event/error codes support diagnosis without payloads.
- `/metrics` exposure is operator-controlled and documented.
- Health is process liveness; readiness is dependency/safety suitability.

## Retention Requirements

- Default terminal payload retention: one hour.
- Default result retention: seven days.
- Default operational/idempotency retention: thirty days.
- Cleanup uses bounded transactions, resumes safely, and emits aggregate counts.
- Active/queued task payload is never purged.
- Expired result has stable API semantics without breaking task metadata.
- Backup documentation treats database content as sensitive.

## Security Requirements

- Loopback default listener and explicit public-bind acknowledgement.
- Strict body/header/time/depth/count limits.
- Admission and database pools remain bounded under load.
- Backend credential is never exposed through API/config output/logs.
- Dependency and container scans have no unresolved critical finding.
- Security policy documents trust boundaries and unsupported direct backend access.

## Procedure

1. Create an explicit data inventory and threat model.
2. Instrument stable sanitized events and bounded metrics.
3. Implement retention worker with test clock and small batches.
4. Add adversarial HTTP/schema/provider body and cardinality tests.
5. Run concurrent load while verifying one active inference and bounded resources.
6. Inject database/backend/worker failures and verify readiness/recovery.
7. Rehearse backup, restore, migration status, and retained idempotency behavior.
8. Run vulnerability/license/dependency scans and document accepted residual risk.

## Automated Acceptance

- Secret/prompt/result/reasoning canary scan finds no leak in logs/metrics/errors.
- Metric label cardinality remains bounded under unique clients/tasks/errors.
- Retention boundary/concurrency/restart tests pass.
- Load tests show one active inference and enforce queue/admission limits.
- Malformed/oversized/fuzz inputs do not panic or exhaust unbounded resources.
- Readiness fails for stale worker, lost lock, schema mismatch, database loss,
  invalid adapter, and indeterminate recovery.
- Backup/restore test preserves required state and isolation.
- Race, vet, build, tests, vulnerability, and whitespace checks pass.

## Manual Acceptance

- Dashboards can be built without sensitive/high-cardinality labels.
- Logs diagnose a task only through operator-supplied non-sensitive correlation.
- Reviewer accepts threat model and residual risks.
- Operator can reduce retention without a code change.

## Stop Conditions

- A required diagnostic depends on logging prompts or raw provider bodies.
- Cleanup can delete queued/running payloads or required idempotency hashes.
- Resource growth is unbounded under a documented input path.
- Critical vulnerability remains without explicit accepted mitigation.
- Readiness reports safe while duplicate execution is possible.

## Handoff

Include threat-model changes, leak/cardinality/load/fault evidence, retention and
restore transcript, scan results, changed files, Git status, and a suggested
commit message. Stop for approval before P09.
