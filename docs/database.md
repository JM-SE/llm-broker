# PostgreSQL Operator Guide

This document covers the broker database: schema, roles, migrations, backup,
restore, and retention. It is operator documentation, not product marketing.

## Supported database

- PostgreSQL 16 and 17 (the versions exercised by the integration suite).
- Schema is plain SQL in `migrations/`; no ORM is involved.

## Roles

Three roles are required, mirroring migration, provisioning, and runtime:

| Role | Uses | Privileges |
| --- | --- | --- |
| Migration role | `llm-broker migrate` | Schema ownership; DDL |
| Provisioner role | `policy diff/apply/status`, `client ...` | Clients, policies, capability rows, and grants; no DDL or slug updates |
| Runtime role | `llm-broker api`, `llm-broker worker` | SELECT-only authorization tables plus task/execution state mutations |

- The runtime role cannot create or alter tables (verified by integration
  tests). A compromise of the API or worker process cannot change the schema.
- Connection URLs arrive from environment variables configured in the
  application config (`url_env`, `migration_url_env`, and
  `provisioner_url_env`); values are never stored in config files.
- Runtime connections should be least-privilege per service: separate roles
  are also reasonable for `api` and `worker` if operator policy requires it.

## Migrations

- Migrations are embedded in the binary (`migrations/` package) and applied
  with the explicit `migrate` command:
  - `llm-broker migrate status [config]` — show applied state and database
    version.
  - `llm-broker migrate up [config]` — apply pending migrations.
- Migrations never run automatically at process startup. The operator decides
  when a schema change is applied, and `status` must be checked first.
- The API/worker refuses to run against an unknown schema version
  (`CheckSchemaCompatible`): a binary/database mismatch fails closed at
  startup instead of running against a schema it does not understand.
- Migration SQL is reviewed like application code. Destructive migrations must
  define their `down` or document an irreversible boundary explicitly.
- H00 migration `00004_h00_workload_capability_authorization.sql` is
  forward-only. Back up first and use planned downtime; existing policies are
  deny-all with null topology digests until `policy apply` succeeds.
- `migrate` operates on exactly the database named by the configured
  environment variable. Point the command at the intended database; it is the
  operator's confirmation step.

## Schema

Tables (see `migrations/00001_initial_schema.sql` for exact DDL):

- `clients` — operator-provisioned identities; only credential digests are
  stored, never tokens.
- `workload_policies` — priority class, limits, retries, retention windows,
  exact timeout, topology digest, allowed generation fields, required adapter
  features.
- `workload_policy_capabilities` — normalized allowlist; zero rows means
  deny-all.
- `client_workload_grants` — which client may submit which workload.
- `tasks` — operational metadata: state, idempotency key, request hash,
  lease, timestamps. The idempotency key and request hash survive payload
  purge.
- `task_payloads` — the client request body, separated from the operational
  row for access control and early purge.
- `task_results` — inference output (content, finish reason, usage).
- `task_attempts` — sanitized attempt records (worker id, state, error class);
  never raw provider bodies or reasoning.
- `worker_heartbeats` — active worker sessions for readiness and stale
  detection.

State and retention invariants are enforced by `CHECK` constraints in the
database, not only by application code: a task in a terminal state must have
`finished_at`, a running task must have `started_at` and a lease, retention
windows must satisfy `payload <= result <= metadata`.

## Retention

Windows are defined per workload policy and honored by bounded cleanup
batches (`CleanupExpired`):

- Request payload: purged after the payload window (default 1 hour).
- Result: purged after the result window (default 7 days).
- Task metadata (including the idempotency key and request hash): purged after
  the metadata window (default 30 days).

Retention is a privacy control: prompts and outputs are sensitive data, so
they are deleted after use, not archived. Cleanup runs in bounded batches and
never removes the idempotency record early: a repeated submission still
replays to the original task while the metadata window is open.

## Backup and restore

Prompts and results are sensitive; backups are sensitive too.

- Point-in-time consistent dumps:
  `pg_dump -Fc` (custom format) for full backups.
- Restore on the same major PostgreSQL version into a dedicated database,
  then run `llm-broker migrate status` to confirm the schema version matches
  the binary before starting API/worker.
- Encrypt and restrict backups like any credential material. Retention is a
  deletion guarantee only for the live database; copies are deleted by the
  operator's backup policy.
- Test restore in a disposable database before relying on it.

## Test database

The integration suite runs against a disposable, dedicated container
(`scripts/pg-test-up.sh` / `scripts/pg-test-down.sh`), loopback-only, on port
55432. It is created fresh per run: schema reset, migrations applied, roles
recreated, and destroyed after evidence is captured. Never point the test
harness at a database that contains real data.

Each package test harness resets the schema, so package test binaries must
run serially: use `go test -p 1 ./...` (also set in CI). Parallel package
execution would race the shared schema resets.

The harness requires `TEST_DATABASE_URL` (migration owner),
`TEST_DATABASE_PROVISIONER_URL` (policy/client provisioner), and
`TEST_DATABASE_RUNTIME_URL` (API/worker runtime). These credentials are never
substituted for one another.

`scripts/pg-test-up.sh` writes these URLs to a temporary mode-0600 environment
file and prints only the file assignment. Load it with `source
"$LLM_BROKER_TEST_ENV_FILE"`; never paste or log the URLs. The down script
removes the environment file when that variable is exported.
