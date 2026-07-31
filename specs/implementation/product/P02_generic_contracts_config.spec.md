# Product Phase P02: Generic Contracts And Configuration

## Status

- Phase: P02.
- State: `accepted`.
- Prerequisite: accepted P01.
- Runtime impact: none; no operational listener or worker.

## Objective

Implement provider-independent domain types, strict configuration, normalized
task contracts, feature declarations, and OpenAPI before persistence.

## In Scope

- Strict versioned JSON configuration.
- API/task domain types and validation.
- Operator-defined workloads, capabilities, backend instances, and aliases.
- Adapter/runtime-controller interfaces without concrete network behavior.
- State transitions and stable error codes.
- Generation and response-contract normalization.
- JSON Schema safety validation.
- Sanitized config examples.
- `openapi.yaml` for client-facing endpoints.

## Out Of Scope

- PostgreSQL, credentials, HTTP server implementation, scheduling, or execution.
- Concrete OpenAI HTTP calls.
- Provider/model/client-specific configuration fields in core types.
- Dynamic plugins.
- Runtime and client integration.

## Deliverables

- `internal/config` loader, validation, redaction, and tests.
- `internal/domain` types, transitions, normalization, and tests.
- `internal/backend` adapter/controller interfaces and feature types.
- `llm-broker config validate` and redacted `config show`.
- `openapi.yaml` with strict request/response/problem schemas.
- Fake-only and generic OpenAI-compatible example configs.

## Configuration Requirements

- Unknown fields and unsupported version fail validation.
- Secret values are references, never inline values in examples.
- Config may declare several backend instances but global concurrency is fixed at
  one and not configurable in v1.
- Each capability resolves to exactly one configured backend.
- Workload policies define priority class, grants, limits, retries, retention
  bounds, allowed generation fields, and required adapter features.
- Backend URLs reject user info, fragments, invalid schemes, and ambiguous forms.
- Config errors identify fields without echoing sensitive values.

## Contract Requirements

- Workload/capability names use bounded stable slug syntax.
- Messages accept only `system`, `user`, and `assistant` text.
- Unknown request fields are rejected.
- Generation is normalized before hashing/persistence.
- JSON Schema is Draft 2020-12, local-only, size/depth/property bounded.
- Trace metadata is scalar, bounded, and explicitly non-sensitive.
- Hidden reasoning has no field in the normalized result type.
- State transitions match the master exactly.

## Procedure

1. Freeze identifiers, limits, and normalized JSON representations.
2. Implement strict config decode and cross-reference validation.
3. Implement redacted config inspection.
4. Define adapter and optional runtime-controller contracts.
5. Implement request normalization and deterministic request hashing input.
6. Implement state-transition validation and broker error taxonomy.
7. Add safe JSON Schema compile/validation boundaries without network loading.
8. Write OpenAPI independent from a named client/provider.
9. Add golden tests for config, normalized requests, and redaction.

## Automated Acceptance

- Unit and fuzz tests cover malformed config and task input.
- Unknown config/API fields fail.
- Equivalent normalized requests produce identical canonical bytes.
- Invalid transitions fail.
- JSON Schema cannot load remote references.
- Redacted output contains no loaded secret values.
- OpenAPI lint/parse and example validation pass.
- Race, vet, build, dependency, and whitespace checks pass.
- Product package/source scan has no integration-specific imports or names.

## Manual Acceptance

- A reviewer can define arbitrary workload/capability/backend names.
- No client-facing schema includes backend or model selection.
- Config makes global serialization explicit and immutable in v1.
- Adapter contract is sufficient for fake and OpenAI-compatible implementations
  without adding provider fields to the domain model.

## Stop Conditions

- Canonicalization or idempotency hashing remains ambiguous.
- An adapter requires exposing provider-specific request fields publicly.
- JSON Schema can trigger network/filesystem access.
- Secret redaction cannot be proven.
- The OpenAPI contract diverges from the master.

## Handoff

### Changed Files

- `internal/domain/`: provider-independent types and validation — `limits.go`,
  `slug.go`, `state.go`, `errors.go`, `priority.go`, `message.go`,
  `generation.go`, `response.go`, `schema_compile.go`, `trace.go`,
  `submission.go`, `result.go` plus unit/fuzz tests.
- `internal/config/`: strict versioned loader — `config.go`, `load.go`,
  `validate.go`, `redact.go` plus unit/fuzz tests.
- `internal/backend/`: adapter and optional runtime-controller contracts —
  `backend.go` plus compile-time contract tests.
- `cmd/llm-broker/`: `config.go` with `config validate` and redacted
  `config show`; `main.go` and `main_test.go` updated.
- `openapi.yaml`: OpenAPI 3.1 client contract (health/ready/metrics, tasks,
  results, cancel, stable problem codes).
- `examples/fake.json` and `examples/openai-compatible.json`: sanitized
  operator configurations.
- `tests/openapi_test.go`: document-level OpenAPI checks.
- `go.mod`/`go.sum`: added `santhosh-tekuri/jsonschema/v6` v6.0.2 and
  `getkin/kin-openapi` v0.145.0 (test-only).
- `specs/.../STATUS.md` and this spec: state updated.

### Validation Evidence

- `go test ./...`: all packages pass.
- `go test -race ./...`: all packages pass.
- `go vet ./...`, `go build ./...`, `gofmt -l .` (empty): clean.
- `go mod verify`: all modules verified; `go mod tidy` stable.
- Fuzz (5s each): `FuzzDecodeTaskSubmission` (425k execs),
  `FuzzNewSchemaContract` (325k), `FuzzDecode` (380k) — no crashes.
- Product scan: no integration/vendor/local identifiers in product source.
- Manual: `config validate` and redacted `config show` verified against both
  example configs.

### Compatibility Questions

- Aging is class-aware: batch may rise to 400 (base 100 + capped 300), normal
  is capped at 300 (never exceeds the high base), high never ages. If a
  different aging curve is wanted for the operator high class, adjust
  `EffectivePriority` before P05.
- kin-openapi does not evaluate `if/then/else` against instances; the
  kind/schema rule of `ResponseContract` is pinned structurally in the OpenAPI
  document and will be enforced behaviorally by the API layer in P04.

### Suggested Commit Message

`feat: P02 generic contracts and configuration (domain, config, backend interfaces, OpenAPI, examples)`

Stop for approval before P03.
