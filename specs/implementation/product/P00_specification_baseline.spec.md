# Product Phase P00: Specification Baseline

## Status

- Phase: P00.
- State: `accepted`.
- Runtime impact: none.
- Approval required before P01.

## Objective

Freeze an implementation-ready, public, backend-agnostic v1 contract and split
generic product delivery from optional local integration.

## Prerequisites

- Empty implementation directory or documentation-only repository.
- Product decisions recorded in the master specification.
- No approval of the superseded host-specific phase plan is assumed.

## In Scope

- Master product architecture and invariants.
- Product phases P00 through P09.
- Integration phases L00 through L04.
- Generic task/API, persistence, scheduler, adapter, recovery, privacy, packaging,
  and release contracts.
- Sanitization and phase-gate rules.
- Status tracker and review handoff.

## Out Of Scope

- Go source, dependency files, OpenAPI, migrations, tests, containers, or CI.
- Apache license file or public project documentation.
- Database/container/service creation.
- Runtime, model, systemd, or client changes.
- Network calls to any inference backend.
- Git commits, tags, or remotes.

## Deliverables

- `specs/llm_broker_v1_master.spec.md`.
- `specs/implementation/00_implementation_phase_index.spec.md`.
- Ten product phase specs under `specs/implementation/product/`.
- Five integration phase specs under `specs/implementation/integrations/`.
- `specs/implementation/STATUS.md`.

## Required Decisions

- Go is the implementation language.
- PostgreSQL is the only durable queue/source of truth.
- Public v1 has one global active inference.
- Built-in adapters are `fake` and `openai_compatible`.
- Other protocols use a documented compile-time Go adapter interface.
- Runtime controllers are optional and separate from inference adapters.
- Workloads, capabilities, backend instances, and model aliases are operator data.
- License is Apache-2.0.
- Distribution includes binaries, Docker image, Compose, and systemd example.
- Product acceptance is independent of every runtime, model, client, and local
  host integration.

## Procedure

1. Remove the superseded host-coupled phase structure.
2. Rewrite the master around product boundaries and public compatibility.
3. Define the P00-P09 product sequence.
4. Define the L00-L04 integration sequence.
5. Ensure every phase has prerequisites, scope, exclusions, deliverables,
   procedure, acceptance, stop conditions, and handoff.
6. Search product specs for integration coupling and private values.
7. Validate Markdown, references, ASCII, Git whitespace, and repository contents.
8. Mark P00 `ready_for_review`; do not start P01.

## Automated Acceptance

- All expected spec files exist and no superseded phase files remain.
- P00-P09 and L00-L04 are each complete and contiguous.
- Every phase spec contains all required gate sections.
- Every referenced phase file resolves.
- Product specs contain no personal paths, secrets, live service ports, or
  dependency on a named integration.
- Documentation is ASCII and `git diff --check` passes.
- Git has no commit created by the phase.

## Manual Acceptance

- Reviewer confirms the public product is useful with fake mode alone.
- Reviewer confirms OpenAI-compatible support is not tailored to one provider.
- Reviewer confirms exactly one inference globally is intentional for v1.
- Reviewer confirms concrete rollout is isolated in the integration track.
- Reviewer confirms later phases cannot modify active client/runtime services
  before the specified gates.

## Stop Conditions

- Product completion depends on a local integration.
- Core API exposes model, URL, runtime command, path, or client priority.
- An integration-specific state leaks into the generic database schema.
- Specs require modifying an active runtime or client in P00.
- A phase is ambiguous about whether real work may be routed.

## Handoff

Report changed files, line count, validation evidence, Git status, unresolved
questions, and a suggested commit message. Stop for explicit approval.
