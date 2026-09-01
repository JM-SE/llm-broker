# Implementation Status

## Current Gate

- Current phase: hardening track H00 `accepted` 2026-08-26 (product track P00-P09 remains accepted).
- State: H00 closed with candidate `96ce3ffb88af4c18a6d55187306193dc9e93e319` tree `45c731cc4b0ac4e084ca1e35a7861a976702f991`; E9 `PASS` independent review `reviews/H00-E9-independent-closure-review-2026-08-26.md`.
- H02: `accepted` 2026-09-01 for MVP on `main` at `2b90452` (test-only dual-route conformance). Gates: `make GO=go check` PASS + focused H02 PASS. Remaining delta gaps are non-blocking `H02-followup` hardening per MVP policy; they do not block close or next-phase planning.
- MVP policy: hardening evidence gaps alone do not block MVP close; a real product-scope defect would reopen planning. Detailed spec: `hardening/H02_generic_dual_route_conformance.spec.md` (external workspace). Next phase planning is now unblocked.
- Integration track L00 remains optional and independent.
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

## Hardening Track

| Phase | State | Approval | Evidence |
| --- | --- | --- | --- |
| H00 | `accepted` 2026-08-26 | approved — E9 PASS `96ce3ffb88af4c18a6d55187306193dc9e93e319` / `45c731cc` | `generated/H00/96ce3ffb88af4c18a6d55187306193dc9e93e319` + `reviews/H00-E9-independent-closure-review-2026-08-26.md` |
| H01 | `accepted` 2026-09-01 | approved — `8068c49b967f56b8778faa8f2670e9b87defe910` / `a05baef7` + correction `d4fef67`; review `PASS` with notes `reviews/H01-post-implementation-review-2026-09-08.md` | `reviews/H01-post-implementation-review-2026-09-08.md` |
| H02 | `accepted` 2026-09-01 — MVP | approved for MVP on `main` at `2b90452`; `make GO=go check` PASS + focused H02 PASS | retained gates: `make check` PASS (race/vet/build/mod/fmt/diff/H00 scans), focused config/API/worker H02 PASS; non-blocking hardening tracked as `H02-followup` per MVP policy |

## Integration Track

| Phase | State | Approval |
| --- | --- | --- |
| L00 | `pending` | unblocked by accepted P09 (independent of hardening) |
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
- Hardening H00: `accepted` 2026-08-26 — candidate `96ce3ffb88af4c18a6d55187306193dc9e93e319` tree `45c731cc4b0ac4e084ca1e35a7861a976702f991`, E1-E8 PASS + E9 PASS `reviews/H00-E9-independent-closure-review-2026-08-26.md`, bundle `generated/H00/96ce3ffb88af4c18a6d55187306193dc9e93e319` frozen; H01 `accepted` 2026-09-01 at `8068c49b967f56b8778faa8f2670e9b87defe910` + correction `d4fef67`, review `PASS` with notes `reviews/H01-post-implementation-review-2026-09-08.md`; H02 `accepted` 2026-09-01 for MVP on `main` at `2b90452` (test-only conformance; `make check` PASS; focused H02 PASS; delta gaps downgraded to non-blocking `H02-followup` per MVP policy).

## MVP Policy

For MVP, hardening evidence gaps alone do not block close. H02 established that
MVP acceptance requires passing gates and no production-scope defect, not
exhaustive hardening proof. Remaining delta gaps are tracked as
`H02-followup` hardening and must not delay next-phase planning unless a real
product defect is demonstrated.

## Suggested Next Commit Message

`docs: close H02 for MVP on main at 2b90452 (H02-followup hardening non-blocking)`
