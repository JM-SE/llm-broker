# Product Phase P06: Backend Adapters

## Status

- Phase: P06.
- State: `accepted`.
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

### Changed Files

- `internal/backend/`: `registry.go` (compile-time Registry, DefaultRegistry,
  `RegisterDefault`), `none_controller.go` (fail-closed optional controller),
  `features.go` (`CheckFeatures`), `registry_test.go`,
  `default_registry_test.go` (external package proving built-in registration).
- `internal/backend/fake/`: deterministic `fake.go` (delay, six fail modes,
  structured kinds return `{}`, no network) plus `fake_test.go` and
  conformance.
- `internal/backend/openaicompat/`: `openaicompat.go` (Chat Completions
  subset mapping, bounded transport with explicit connect/header/overall
  budgets, redirects disabled, credential from env at construction,
  reasoning discarded, conservative classification) plus
  `openaicompat_test.go`, `parse_fuzz_test.go`.
- `internal/backend/conformance/`: shared `conformance.go` suite (type
  stability, normalized responses, classified errors, cancellation).
- `docs/backends/adapter-development.md`: author guide and testing checklist.

### Validation Evidence

- `go test -race -p 1 ./...`: all 12 packages pass with integration DB.
- `go vet ./...`, `go build ./...`, `gofmt -l .` (empty): clean.
- Fuzz: `FuzzParseChatCompletion` 8s (one false-positive assertion fixed:
  mutated inputs moved seed strings into content; the leak invariant is
  structural — the response type has no reasoning field), plus existing
  domain/config fuzz targets pass.
- HTTP fixtures: golden request (model/messages/generation fields present;
  frequency_penalty/presence_penalty/response_format/stream absent),
  reasoning_content discarded, 400/401/404/429/500/502/301 classification,
  malformed/oversized bodies, connect-refused (transient_unsent), slow
  server and mid-body disconnect and cancellation (indeterminate),
  structured output gated on declared feature.
- No test uses the public network; no real model was called.

### Compatibility Questions

- `New` reads the credential from `credential_env` at construction time;
  the worker must set those variables before constructing adapters (P07).
- The default registry is populated by `init()` of the built-in packages;
  the worker binary must blank-import them (P07 wiring).
- `check` fail modes return the configured class without simulating
  transport phases; transport-failure classification is exercised by the
  openai_compat fixtures.
- Redirect handling returns the 3xx status to the classifier
  (`unexpected_status`, backend_rejected) — credentials never cross origins.

### Suggested Commit Message

`feat: P06 backend adapters (registry, deterministic fake, OpenAI-compatible mapping, conformance suite)`

Stop for approval before P07.
