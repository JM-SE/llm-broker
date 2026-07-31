# Release Notes (v1 candidate)

## Supported Matrix

- Go 1.26.5 (toolchain pinned in `go.mod`).
- PostgreSQL 16 and 17 (integration-tested against 17).
- Target platforms: `linux/amd64`, `linux/arm64` (static binaries).
- Runtime: any Linux host or container; the container image runs as an
  unprivileged user with a minimal Alpine runtime.

## Version Metadata

`llm-broker version` reports version, Go version, commit, and build time.
Release binaries and the container image are built with the same ldflags
from the same commit (`make release`, `make image`).

## Release Checklist

- [ ] `make check` green from a clean checkout.
- [ ] `go test -race -p 1 ./...` green against a disposable PostgreSQL
      (`scripts/pg-test-up.sh`; always `-p 1` — integration packages share
      the disposable database).
- [ ] Fuzz targets run: `FuzzDecodeTaskSubmission`, `FuzzNewSchemaContract`,
      `FuzzDecode`, `FuzzParseChatCompletion`.
- [ ] `govulncheck ./...` reports no vulnerabilities.
- [ ] Container builds and passes the fake-mode smoke test
      (`docker compose build` + docs/quickstart.md flow).
- [ ] Restore rehearsal passes (`scripts/restore-rehearsal.sh`).
- [ ] `make release` produced archives + `SHA256SUMS` for both platforms.
- [ ] Product source scan: no integration-specific identifiers
      (`grep -rInE "llama.?swap|qwen|ytpipe|/home/|0.0.0.0" internal/ cmd/`).
- [ ] License text and notices shipped (LICENSE in archives and image).

## Dependency Inventory

Direct runtime dependencies (all common open-source licenses; full module
list via `go list -m all`):

| Module | Purpose | License |
| --- | --- | --- |
| jackc/pgx/v5 | PostgreSQL driver | MIT |
| pressly/goose/v3 | embedded SQL migrations | Apache-2.0 |
| google/uuid | task identifiers | BSD-3-Clause |
| santhosh-tekuri/jsonschema/v6 | client schema validation | Apache-2.0 |
| prometheus/client_golang | metrics | Apache-2.0 |
| getkin/kin-openapi | OpenAPI validation (tests only) | MIT |

Full transitive list: `go list -m all`. Licenses of transitive modules are
available in the Go module cache; the direct dependencies above are the
only ones linked into the binary.

## Publication (NOT EXECUTED)

The following are intentionally **not** performed during the review gate:

- `git tag v0.1.0` and `git push --tags` (release workflow then builds
  artifacts as CI artifacts).
- GitHub Release creation with binaries/checksums.
- `docker push` of the image.
- `go install` publication.

Run them only after explicit approval of this phase.

## Product v1 Invariants (recap)

- Exactly one global inference at a time; only the worker invokes backends.
- Clients submit workload/capability/messages only — never models, URLs,
  priorities, or provider parameters.
- Reasoning content is never persisted, logged, or returned.
- Ambiguous outcomes block all inference until operator resolution.
- Migrations are explicit; services never auto-migrate.
