# Implementation Status

## Current Gate

- Current phase: product track complete (P00-P09 accepted).
- State: product v1 accepted; integration track available.
- Next optional phase: L00 (sanitized llama-swap/Qwen profile), only when the operator starts the integration track.
- Git policy: commits are operator-controlled unless explicitly delegated for a phase.

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
| P08 | `accepted` | approved (P08 gate waived per user instruction) |
| P09 | `accepted` | approved |

## Integration Track

| Phase | State | Approval |
| --- | --- | --- |
| L00 | `pending` | unblocked by accepted P09 |
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

## Validation Evidence

- Product phases P00-P09 implemented and accepted.
- Automated suite: `go test -race -p 1 ./...` across product packages.
- Packaging smoke: Docker Compose fake-mode end-to-end (migrate, seed, client
  create/grant, POST Prefer wait, ready/metrics).
- Release artifacts: linux amd64/arm64 tarballs + SHA256SUMS generated locally.
- Product acceptance does not require YTPipe, llama-swap, or any named local
  model/runtime.

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
- Product P08: accepted (bounded metrics, sanitized event logging,
  retention job, threat model, restore rehearsal).
- Product P09: accepted (packaging and release candidate, Docker, compose
  fake-mode, systemd examples, release artifacts).
- Product track complete. Integration track optional and independent.

## Suggested Next Commit Message

`docs: accept P09 and close product track P00-P09`
