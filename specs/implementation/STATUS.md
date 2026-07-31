# Implementation Status

## Current Gate

- Current phase: P08, Security, Privacy, and Observability.
- State: `ready_for_review`.
- Previous phases: P00 through P07 accepted.
- Git policy: no automatic commits; P07 was committed by the assistant on
  explicit user request, otherwise changes are left for manual review and
  user commits.

## Product Track

| Phase | State | Approval |
| --- | --- | --- |
| P00 | `accepted` | approved |
| P01 | `accepted` | approved |
| P02 | `accepted` | approved |
| P03 | `accepted` | approved |
| P04 | `accepted` | approved |
| P05 | `accepted` | approved |
| P06 | `accepted` | approved |
| P07 | `accepted` | approved |
| P08 | `ready_for_review` | P07 accepted |
| P09 | `blocked` | requires P08 |

## Integration Track

| Phase | State | Approval |
| --- | --- | --- |
| L00 | `blocked` | requires accepted P09 |
| L01 | `blocked` | requires L00 |
| L02 | `blocked` | requires L01 and maintenance approval |
| L03 | `blocked` | requires L02 and cutover approval |
| L04 | `blocked` | requires L03 and soak approval |

## P00 Scope Record

- Replaced the superseded host-coupled 00-13 plan with product P00-P09 and
  integration L00-L04 tracks.
- Defined public v1 as generic Go/PostgreSQL infrastructure.
- Defined built-in `fake` and `openai_compatible` adapters and compiled extension
  contract.
- Separated optional runtime control from inference protocol adapters.
- Kept exactly one global inference as a public v1 invariant.
- Made capabilities, workloads, backend instances, and model aliases operator
  configuration.
- Isolated llama-swap, Qwen, local-host deployment, and YTPipe in sanitized
  integration phases.
- Made product completion independent from all integration phases.

## P00 Runtime Record

- No Go source or dependency was created.
- No database/container/service was created or modified.
- No systemd unit, runtime, listener, model, or client was changed.
- No inference/network request was made.
- No Git commit, tag, remote, or staging action was performed.

## Validation Evidence

- Documentation set: 18 files and 2424 lines at validation.
- Phase set: exactly P00-P09 and L00-L04; all 15 index references resolve.
- Structure: every phase has status, objective, prerequisite/precondition, scope,
  exclusions, deliverables, procedure, automated/manual acceptance, stop
  conditions, and handoff.
- State: one phase is `ready_for_review`; fourteen phases are `pending` and
  blocked by the index gates.
- Sanitization: no personal home path, known live port, concrete local model
  filename, local container/database identity, or credential was found.
- Product isolation: product phase specs contain no llama-swap, Qwen, YTPipe,
  personal path, or concrete listener reference.
- Formatting: ASCII-only; no trailing whitespace; `git diff --check` and cached
  equivalent pass.
- Repository: only `.git/` and `specs/`; branch `main`; no commits or staging;
  `specs/` remains untracked for review.

## Approval Log

- Initial host-specific Phase 00: superseded before approval.
- Generic Product P00: accepted.
- Product P01: accepted.
- Product P02: accepted (contracts, configuration, backend interfaces,
  OpenAPI).
- Product P03: accepted (PostgreSQL persistence, migrations, store,
  integration tests).
- Product P04: accepted (API, authentication, idempotency, bounded wait,
  cancel intent).
- Product P05: accepted (scheduler, aging, affinity batches, leadership,
  single claim path).
- Product P06: accepted (backend adapters, registry, deterministic fake,
  OpenAI-compatible mapping, conformance suite).
- Product P07: accepted (worker and recovery, serial execution, persisted
  backoff, cancel semantics, ambiguous recovery CLI).
- Product P08: ready for review (bounded metrics, sanitized event logging,
  retention job, threat model, restore rehearsal).

## Suggested Commit Message

`feat: P08 security, privacy, and observability (bounded metrics, sanitized events, retention job, threat model, restore rehearsal)`
