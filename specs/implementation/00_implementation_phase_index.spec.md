# Implementation Phase Index Specification

## Goal

Deliver a public, backend-agnostic broker first, then validate the separate
sanitized llama-swap/Qwen/YTPipe integration without making it a product
dependency.

## Execution Rules

- Product phases use `P00` through `P09` and execute strictly in order.
- Integration phases use `L00` through `L04` and begin only after P09 acceptance.
- Every phase stops in `ready_for_review` and requires explicit approval.
- A later phase must not begin while its prerequisite is unaccepted.
- Every handoff includes changed files, Git status, diff summary, tests/results,
  manual checklist, known risks, and a suggested commit message.
- Git commits are not automatic.
- Scope discoveries update specs visibly; they are not silently implemented.
- Product code and schema may not depend on a named integration.

## Product Track

| Phase | Specification | Outcome |
| --- | --- | --- |
| P00 | `product/P00_specification_baseline.spec.md` | Generic product contract and two-track plan |
| P01 | `product/P01_oss_go_foundation.spec.md` | Licensed public Go repository and safe binary skeleton |
| P02 | `product/P02_generic_contracts_config.spec.md` | Generic API, config, capability, and domain contracts |
| P03 | `product/P03_postgres_persistence.spec.md` | Portable PostgreSQL migrations and store |
| P04 | `product/P04_api_auth_idempotency.spec.md` | Authenticated durable task API |
| P05 | `product/P05_scheduler_claiming.spec.md` | Serial scheduler, aging, batches, lock, and claims |
| P06 | `product/P06_backend_adapters.spec.md` | Fake and OpenAI-compatible adapters plus extension contract |
| P07 | `product/P07_worker_recovery.spec.md` | Durable serial worker, retries, cancellation, and recovery |
| P08 | `product/P08_security_observability.spec.md` | Privacy, retention, metrics, logs, and fault hardening |
| P09 | `product/P09_packaging_release.spec.md` | Binary/Docker/Compose public release candidate |

## Integration Track

| Phase | Specification | Outcome |
| --- | --- | --- |
| L00 | `integrations/L00_llama_swap_qwen_profile.spec.md` | Sanitized llama-swap/Qwen profile and dormant assets |
| L01 | `integrations/L01_ytpipe_adapter_offline.spec.md` | Disabled and undeployed YTPipe adapter |
| L02 | `integrations/L02_runtime_rehearsal.spec.md` | Synthetic real-runtime rehearsal followed by restoration |
| L03 | `integrations/L03_controlled_cutover.spec.md` | Approved canary and controlled activation |
| L04 | `integrations/L04_stabilization.spec.md` | Soak, recovery evidence, and legacy cleanup decision |

## Dependencies

- P00 is required before all implementation.
- P01 establishes public repository/toolchain controls.
- P02 freezes generic contracts before persistence and HTTP implementation.
- P03 provides durable storage for P04 and later phases.
- P04 persists and exposes tasks before scheduling.
- P05 proves global exclusion and claims before execution.
- P06 completes provider-independent adapter behavior before the worker uses it.
- P07 completes fake end-to-end execution before hardening.
- P08 must pass before packaging and release acceptance.
- P09 completes product v1 independently of local integration.
- L00 requires P09 and prepares a concrete but sanitized profile.
- L01 requires L00 and changes only undeployed client source.
- L02 requires L01, an approved maintenance window, and mandatory restoration.
- L03 requires accepted rehearsal evidence and separate cutover approval.
- L04 begins only after accepted canary/bounded traffic.

## Product Agnosticism Gates

- Product specs and packages may mention adapter types, never a required runtime
  vendor, concrete model, local service, personal path, or client application.
- Product tests use fake or local protocol test servers only.
- `openai_compatible` is the only real protocol adapter required by public v1.
- Capabilities, workloads, backend instances, and model aliases are operator data.
- Global concurrency remains exactly one in v1.
- Integration examples cannot alter core API/state/scheduler semantics.

## Integration Safety Gates

- L00 creates sanitized repository assets only and installs nothing.
- L01 may modify a client working tree only after explicit approval; it does not
  deploy or connect it.
- L02 is the first phase allowed to contact a real model runtime and always
  restores the pre-rehearsal runtime.
- L03 is the first phase allowed to route real client work through the broker.
- L04 preserves rollback until a separate legacy-removal approval.

## Phase Completion Contract

A phase is `ready_for_review` only when:

- All in-scope artifacts exist.
- Automated checks pass or a blocker is explicit.
- Manual checks are complete where locally verifiable.
- No later-phase work was introduced.
- `specs/implementation/STATUS.md` records evidence.
- The handoff identifies unrelated or unexpected workspace changes.

Only explicit human approval changes a phase to `accepted`.

## Stop Conditions

Stop when:

- A master invariant cannot be satisfied.
- Product code becomes dependent on an integration.
- A migration risks data outside the selected broker database.
- A test indicates more than one active inference.
- Backend idleness cannot be proven after an indeterminate outcome.
- Client work cannot be reconciled before rehearsal/cutover.
- An action would cross the current phase boundary.
