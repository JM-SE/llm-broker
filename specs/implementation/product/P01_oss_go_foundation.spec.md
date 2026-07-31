# Product Phase P01: OSS And Go Foundation

## Status

- Phase: P01.
- State: `ready_for_review`.
- Prerequisite: accepted P00.
- Runtime impact: none.

## Objective

Create the licensed public repository foundation and a safe, testable Go binary
that has no database or backend side effects.

## In Scope

- Apache-2.0 license.
- Go module and pinned stable direct dependencies needed by this phase.
- `cmd/llm-broker` with version, help, and placeholder command dispatch.
- Minimal internal build/version/config logging foundations.
- README, SECURITY, CONTRIBUTING, Code of Conduct decision, and `.gitignore`.
- Makefile or documented direct Go commands with no hidden installer behavior.
- Initial CI for formatting, tests, vet, and build.
- Repository policy for generated files, commits, releases, and secrets.

## Out Of Scope

- Database connection or migrations.
- HTTP listener, authentication, task contract, scheduler, or worker.
- Backend adapters and network calls.
- Docker/Compose release artifacts beyond optional placeholders.
- Runtime/client integration.
- Automatic commit, push, release, or remote creation.

## Deliverables

- `LICENSE`, `README.md`, `SECURITY.md`, `CONTRIBUTING.md`, `.gitignore`.
- `go.mod`, `go.sum`.
- `cmd/llm-broker/main.go` and minimal supporting packages.
- Build/version metadata injected without making local builds fail.
- Unit tests for command selection and redacted startup/version output.
- CI workflow using a pinned Go toolchain/action major.

## Procedure

1. Recheck the current stable Go release and direct dependency compatibility.
2. Install/use the approved local Go toolchain without replacing unrelated tools.
3. Initialize the module as `github.com/JM-SE/llm-broker`.
4. Add only dependencies actually imported in P01.
5. Implement `--help`, `version`, and safe errors for unimplemented commands.
6. Add signal-safe process scaffolding without goroutine background work.
7. Add public governance/security documentation and Apache-2.0 notice guidance.
8. Add CI and local verification commands.
9. Ensure output contains no environment dump, credentials, or host-specific data.

## Automated Acceptance

- `gofmt` produces no diff.
- `go test ./...` passes.
- `go test -race ./...` passes.
- `go vet ./...` passes.
- `go build ./cmd/llm-broker` passes.
- `go mod verify` passes.
- Help/version commands exit predictably.
- Secret scanner finds no committed credential.
- `git diff --check` passes.

## Manual Acceptance

- README describes a generic durable serial broker and does not claim unbuilt
  functionality.
- License/copyright naming is approved.
- Security policy gives a private disclosure route before publication.
- The binary performs no network or persistent action without an explicit
  operational subcommand.

## Stop Conditions

- Go/dependency versions require beta or unsupported releases.
- Module identity or copyright owner is unresolved.
- Foundation starts database, listener, worker, or backend implicitly.
- A public file contains a private path, secret, or deployment-specific identity.

## Handoff

Include toolchain/dependency versions, commands run, generated binary status,
files changed, Git status, known supply-chain risks, and a suggested commit
message. Stop for approval before P02.
