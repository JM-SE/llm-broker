# Product Phase P09: Packaging And Release Candidate

## Status

- Phase: P09.
- State: `pending`.
- Prerequisite: accepted P08.
- Runtime impact: disposable packaging/Compose environment only.

## Objective

Produce a reproducible, documented public v1 release candidate that works in fake
mode and can target a generic tested OpenAI-compatible endpoint.

## In Scope

- Production build/version metadata.
- Multi-stage non-root Docker image.
- Compose example with dedicated PostgreSQL and fake mode default.
- Parameterized systemd example.
- Migration/credential/backup/restore/upgrade/operator guides.
- CI release checks and artifact/checksum generation.
- Public compatibility/limitations documentation.
- Final product acceptance independent of local integration.

## Out Of Scope

- Publishing a tag, GitHub release, package, or image without explicit approval.
- Real model/runtime calls.
- Local host installation.
- Any named client modification.
- Integration profile execution.

## Deliverables

- `Dockerfile`, `.dockerignore`, `compose.example.yaml`.
- Parameterized `deploy/examples/systemd/` assets.
- Fake-mode quick start and generic OpenAI-compatible guide.
- Operations, migration, backup/restore, upgrade, rollback, and troubleshooting
  documents.
- Release workflow/config and local artifact/checksum commands.
- Software/version/compatibility inventory and release checklist.

## Packaging Requirements

- Image runs as non-root with minimal runtime contents and read-only compatibility.
- Image contains the single binary, notices, and no source/build secrets.
- Compose binds API/database safely by default and uses fake backend.
- Compose secrets use placeholders/environment or mounted files.
- Health checks distinguish API liveness/readiness appropriately.
- systemd example has dedicated user, explicit environment/config, hardening,
  restart policy, separate API/worker units, and migration runbook.
- Binary archives and image expose identical version/commit metadata.

## Documentation Requirements

- Quick start creates no contact with a real model.
- OpenAI-compatible guide states feature variability and fail-closed recovery.
- Exactly-one global invariant and non-goals are prominent.
- Upgrade never auto-migrates on service startup.
- Backup/restore includes sensitive-data warnings.
- Adapter author guide and public API/config references are linked.
- Security disclosure and Apache-2.0 notices are complete.

## Procedure

1. Freeze supported Go/PostgreSQL/platform matrix for v1.
2. Add reproducible binary build and version metadata.
3. Build and inspect the non-root image and software inventory.
4. Implement fake-mode Compose with disposable named resources.
5. Add parameterized systemd and generic backend examples.
6. Run clean-machine/container quick start, migration, token, submit, poll, cancel,
   retention, backup/restore, upgrade, and rollback rehearsals.
7. Run full CI/security/license/container checks.
8. Generate local release artifacts and checksums; do not publish.
9. Verify no product file depends on any integration profile.

## Automated Acceptance

- Full test/race/vet/build/fuzz-selected suite passes from clean checkout.
- Container builds, runs non-root, and passes fake end-to-end smoke tests.
- Compose starts from documented commands with only generated local secrets.
- Migration/upgrade/restore smoke tests pass.
- Binaries/checksums are generated for approved target platforms.
- Image/filesystem scan finds no credential, private path, build cache, or critical
  vulnerability.
- License/notice/dependency checks pass.
- Product source/schema scan has no integration dependency.

## Manual Acceptance

- A new operator can complete fake quick start from documentation.
- A contributor understands exact OpenAI-compatible compatibility limits.
- Reviewer accepts release checklist, supported platforms, and residual risks.
- Product acceptance does not require L00-L04.
- No artifact/tag/image is published during the review gate.

## Stop Conditions

- Quick start requires a real runtime or local host knowledge.
- Image runs as root without accepted justification.
- Compose exposes PostgreSQL or backend publicly by default.
- Release artifacts contain secrets/private paths or inconsistent version data.
- Product tests import or invoke integration-specific code.

## Handoff

Include clean-start transcript, artifact/checksum list, image identity/scan,
supported matrix, all checks, Git status, publication commands not yet executed,
and a suggested commit/tag message. Mark product v1 `ready_for_review` and stop.
