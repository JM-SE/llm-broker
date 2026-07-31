# Product Phase P06: Backend Adapters

## Status

- Phase: P06.
- State: `pending`.
- Prerequisite: accepted P05.
- Runtime impact: fake and local `httptest` servers only.

## Objective

Implement a reusable adapter conformance boundary plus the built-in deterministic
fake and generic OpenAI-compatible Chat Completions adapters.

## In Scope

- Adapter request/response/error interfaces.
- Optional runtime-controller interface and `none` implementation.
- Adapter registration at compile time.
- Deterministic fake backend/fault modes.
- OpenAI-compatible request mapping and response normalization.
- Feature declarations and workload compatibility checks.
- Transport deadlines, origin/credential safety, reasoning removal, and tests.
- Public adapter author documentation and conformance suite.

## Out Of Scope

- Calling any real model endpoint.
- Provider-specific runtime management/status semantics.
- Dynamic plugins.
- Worker loop/state transitions/retries.
- Streaming, tools, images, audio, embeddings, or responses API.

## Deliverables

- `internal/backend` interfaces, registry, normalized errors, and tests.
- `internal/backend/fake` deterministic adapter.
- `internal/backend/openaicompat` Chat Completions adapter.
- `none` runtime controller.
- `docs/backends/adapter-development.md`.
- Shared conformance test package usable by future adapters.

## Adapter Requirements

- Adapter receives only normalized broker request plus operator-resolved config.
- Adapter returns final content, normalized usage, and finish reason.
- Adapter discards hidden reasoning before returning.
- Error classes distinguish definitely-unsent, safe transient, rejected,
  malformed/output, indeterminate, and internal.
- Errors expose sanitized codes/details, not raw body or arbitrary provider text.
- Context cancellation and all transport phase timeouts are honored.
- Adapter never updates persistence or retries independently.

## OpenAI-Compatible Requirements

- URL/model/credential come only from validated operator configuration.
- Supports Chat Completions messages and declared normalized generation fields.
- Omits unsupported parameters rather than guessing provider behavior.
- Structured response mapping occurs only when declared supported.
- Redirects are disabled by default; credentials never cross origins.
- HTTP status/body parsing is bounded.
- A disconnect after the request may have been accepted is indeterminate.
- Compatibility documentation lists tested semantics, not universal claims.

## Fake Requirements

- Deterministic response content and usage fixtures.
- Configurable bounded delay.
- Modes: success, provider rejection, malformed output, unsent failure, safe
  transient, and indeterminate failure.
- Cancellation and deadline behavior are deterministic.
- No network access.

## Procedure

1. Freeze adapter/controller interfaces from P02 implementation evidence.
2. Build conformance cases before concrete adapters.
3. Implement fake adapter and every fault mode.
4. Implement an HTTP transport with explicit connect/write/read/overall budgets.
5. Implement OpenAI-compatible mapping/parsing and feature gates.
6. Test all behavior with `httptest`, including slow/disconnected servers.
7. Add reasoning/redaction and adversarial error-body tests.
8. Document compile-time registration and compatibility requirements.

## Automated Acceptance

- Both built-in adapters pass shared conformance tests.
- Fake tests are deterministic under race detector.
- OpenAI-compatible golden requests omit unsupported/private fields.
- Hidden reasoning is absent from response objects, errors, logs, and snapshots.
- Redirect/origin credential tests pass.
- Oversized/malformed/error provider responses are bounded and sanitized.
- Timeout and disconnect tests produce conservative classes.
- No test requires public network or real model.
- Fuzz, race, vet, build, dependency, and whitespace checks pass.

## Manual Acceptance

- A contributor can identify all files/steps needed for a new compiled adapter.
- Generic config can target different compliant servers without source changes.
- Runtime controller is visibly optional and cannot bypass global scheduling.

## Stop Conditions

- Provider quirks leak into public task/domain types.
- Adapter retries or mutates database state itself.
- Connection closure is treated as proof that generation stopped.
- Raw provider body/reasoning can reach logs or client-visible errors.
- Compatibility depends on a named runtime not tested in this phase.

## Handoff

Include conformance matrix, HTTP fixture evidence, timeout/error classification,
reasoning/redaction evidence, changed files, Git status, and a suggested commit
message. Stop for approval before P07.
