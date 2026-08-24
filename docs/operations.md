# Operations

## Retention

Defaults (per workload policy): terminal request payload 1 hour, result 7
days, operational/idempotency metadata 30 days. Retention is stored per
workload policy in `workload_policies.payload_retention_seconds`,
`result_retention_seconds`, and `metadata_retention_seconds`; the ordering
invariant payload <= result <= metadata is enforced by the database.

The active worker runs a bounded retention sweep every 10 minutes
(batches of 1000 rows). Sweeps are restart-safe: cutoffs are re-derived
from the policy rows, and queued/running payloads are never eligible.
Aggregate counts are logged and exposed as `llm_broker_retention_purged_total`.

To change retention without a code change, update the versioned operator
configuration and project it through the provisioner workflow:

```bash
llm-broker policy validate /etc/llm-broker/config.json
llm-broker policy diff /etc/llm-broker/config.json
llm-broker policy apply /etc/llm-broker/config.json
llm-broker policy status /etc/llm-broker/config.json
```

Never mutate `workload_policies` or its capability table directly. Direct SQL
bypasses canonicalization, advisory locks, topology checks, and audit-friendly
policy reports.

The idempotency fingerprint (key + request hash) survives payload purge, so
replayed requests keep returning the original task after the payload is
gone.

## Backup And Restore

The database is the source of truth and contains sensitive material
(prompts, results). Backups must be treated as secrets.

Rehearse against a disposable database before any real operation:

```bash
scripts/restore-rehearsal.sh
```

The script dumps the disposable test database, creates a fresh database,
restores into it, and verifies the migration version and task state survive.
Real restore procedure:

1. `pg_dump -Fc <db> > backup.dump` (or the container equivalent).
2. Restore into a same-major-version PostgreSQL: `pg_restore -d <newdb> backup.dump`.
3. Run `llm-broker migrate status` against the restored database and verify
   the version matches the binary (`llm-broker version` + migration files).
4. Start the worker and confirm `/ready` returns 200 before resuming
   client traffic.

## Upgrades

1. Back up the database and stop every old API/worker.
2. Install the H00 binary and configuration v2.
3. For an existing database volume, create the provisioner and runtime roles
   first with the migration-owner environment and
   `deploy/compose/bootstrap-roles.sh`. Fresh Compose volumes create these
   roles automatically through `init-roles.sh`. Do not put passwords in shell
   arguments or logs.
4. `llm-broker migrate status` then `llm-broker migrate up` with the migration
   role (forward-only; no down migration).
5. Run `policy validate`, `policy diff`, `policy apply`, and `policy status`
   with the provisioner role. Do not start API/worker before apply succeeds.
6. Start the worker, then the API, and verify `/ready` plus a disposable fake
   smoke task before real traffic. Mixed old/new binaries are unsupported.

The former `deploy/compose/seed.sql` hand-seeding procedure is unsupported and
has been removed. Policy state must be projected through `policy apply`.

## Rollback

1. Stop the worker first (`systemctl stop llm-broker-worker`) so no new
   claims happen during the window.
2. Deploy the previous binary.
3. If a migration was applied: restore the pre-upgrade backup into a fresh
   database of the same major version, verify `migrate status` matches the
   previous binary, then point configuration at it. Forward-only policy:
   never attempt to hand-edit `goose_db_version`.
4. Verify `/ready` and one fake smoke task before resuming traffic.

## Troubleshooting

| Symptom | Cause / action |
| --- | --- |
| `/ready` 503 "no active worker" | worker down or heartbeat stale; check `systemctl status llm-broker-worker` and logs |
| `/ready` 503 "ambiguous execution" | run `llm-broker worker status`, verify backend idle, `worker resolve` |
| Tasks stay queued | worker blocked (see above) or lock lost; check `llm_broker_worker_leadership` |
| `migrate status` mismatch at startup | schema version differs from binary; run `migrate up` explicitly after review |
| Task failed `transient_unsent` | backend unreachable when the request was sent; check backend health |
| Task failed `indeterminate` | outcome unknown; inspect with `worker status`, resolve with backend evidence |
| API 503 on create | readiness failing; the API refuses to accept work it cannot schedule reliably |

## Fault Response

- Worker process dies: systemd restarts it; leases expire; the queue is
  preserved. If the worker died mid-inference, the task becomes recovery
  work — inspect with `llm-broker worker status` and resolve only with
  backend idle evidence.
- Ambiguous outcome (indeterminate): the worker blocks ALL new inference.
  `llm-broker worker status` lists the task; verify the backend is idle,
  then `llm-broker worker resolve <task-id> --outcome=requeue|failed`.
- Database loss: `/ready` returns 503; the API keeps answering `/health`.
- Backend down: tasks fail with `transient_unsent` (never sent) and retry
  with persisted backoff, or `indeterminate` (possibly sent) and block.
