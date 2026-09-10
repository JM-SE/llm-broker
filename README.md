# LLM Broker

One queue for every app. One inference at a time. Nothing lost on restart.

LLM Broker is a durable, single-host inference scheduler for applications that
share local or privately hosted language-model servers. If you run several
apps against one GPU box — a YouTube summarizer, a job-search pipeline, a chat
assistant — the broker sits in front of the model runtime and makes sharing
safe: clients enqueue work, the broker decides what runs next, and exactly one
inference executes globally.

It is independent infrastructure software. It does not depend on any specific
model, runtime, or client application.

## Why it exists

Sharing a local model server without a broker means every app implements its
own queue, its own retries, and its own idea of priority — and they still
collide on the GPU. LLM Broker centralizes that problem in one place:

- **Exactly one inference globally.** A deliberate product guarantee, not a
  deployment limitation. The active worker holds a PostgreSQL advisory lease;
  standby workers never claim.
- **Durable queue.** Every inference is a committed PostgreSQL task. Restarts,
  crashes, and client disconnects never lose work.
- **Broker-owned scheduling.** Clients submit `workload` and `capability`
  names only. The broker resolves priority class, aging, model affinity, and
  backend routing from operator configuration. Clients never pick models,
  backends, or priorities.
- **Privacy-aware.** Prompts, results, credentials, and reasoning are never
  logged, persisted in operational tables, or exposed in metrics. Backups are
  treated as secrets.
- **Boring operations.** Single Go binary, explicit migrations, loopback by
  default, Prometheus metrics, no auto-migrate on startup.

## How it works

```text
apps ── POST /v1/tasks ──> API ── durable task ──> PostgreSQL
                              │                          │
                              │              worker (lease holder)
                              │               claims ONE task
                              │               runs ONE inference
                              v                          v
                         GET /v1/tasks/{id}/result <── result row
```

1. A client submits a task: `workload`, `capability`, `messages`, and the
   desired response shape, with an `Idempotency-Key` header.
2. The API validates, authenticates, and durably persists the task
   (`201 Created`, or `200` with the result if you asked for a bounded
   `Prefer: wait=N` and it finished in time).
3. The single active worker claims the highest-priority eligible task
   (`FOR UPDATE SKIP LOCKED` — each task claimed exactly once), runs one
   bounded inference through a backend adapter, and stores the result.
4. The client polls `GET /v1/tasks/{id}` and `GET /v1/tasks/{id}/result`, or
   cancels with `POST /v1/tasks/{id}/cancel`.

Scheduling is deterministic and documented: effective priority is
`class_base + aging_boost` (`high` 300 with no aging, `normal` 200, `batch`
100), ties break by oldest task first. Same-capability affinity batches (up to
4 claims or 10 minutes) keep the model hot, but a higher-priority task always
preempts before the next claim. See [docs/scheduler.md](docs/scheduler.md).

## Quick start (fake mode, no model needed)

The fastest way to see the whole loop. You need Docker with Compose.

```bash
git clone https://github.com/JM-SE/llm-broker.git
cd llm-broker
cp compose.example.yaml compose.yaml
export LLM_BROKER_DB_PASSWORD="$(openssl rand -hex 24)"
export LLM_BROKER_PROVISIONER_PASSWORD="$(openssl rand -hex 24)"
export LLM_BROKER_RUNTIME_PASSWORD="$(openssl rand -hex 24)"
docker compose build
docker compose run --rm migrate migrate up
docker compose run --rm policy-apply policy apply
docker compose up -d worker
docker compose up -d api
```

Provision a client credential (written only to a mode-0600 file, never
printed) and grant it a workload:

```bash
credential_dir="$(mktemp -d)" && chmod 700 "$credential_dir"
docker compose run --rm --user "$(id -u):$(id -g)" \
  -v "$credential_dir:/credentials:rw" policy-apply client create demo \
  --credential-file /credentials/demo.credential
chmod 600 "$credential_dir/demo.credential"
docker compose run --rm policy-apply client grant demo default
```

Submit a task and wait up to 30 seconds for the terminal envelope. The
credential is passed via a temporary curl config file so the bearer value
never appears in the process list (same practice as
[docs/quickstart.md](docs/quickstart.md)):

```bash
curl_cfg="$(mktemp)" && chmod 600 "$curl_cfg"
printf 'header = "Authorization: Bearer %s"\n' \
  "$(< "$credential_dir/demo.credential")" >"$curl_cfg"
trap 'rm -f "$curl_cfg"' EXIT
curl -sS -X POST http://127.0.0.1:8010/v1/tasks \
  --config "$curl_cfg" \
  -H "Idempotency-Key: demo-task-0001" \
  -H "Prefer: wait=30" \
  -H "Content-Type: application/json" \
  -d '{
    "workload": "default",
    "capability": "instruct",
    "messages": [{"role": "user", "content": "Hello"}],
    "response": {"kind": "text"}
  }'
rm -rf "$credential_dir"
```

The `fake` backend answers deterministically, so you get a `"status":
"succeeded"` envelope with fake content. Full walkthrough, including polling,
cancellation, and `/ready` checks, is in
[docs/quickstart.md](docs/quickstart.md).

## Using a real model

Point the broker at any tested OpenAI-compatible `chat/completions` server.
Provide operator configuration and credentials — clients still only send
`workload` + `capability`:

```bash
make build   # single static binary; needs Go 1.26+
```

Then select operator configuration via `LLM_BROKER_CONFIG_FILE` — the bundled
`examples/openai-compatible-summary.json` is a generic starting point:

Set `base_url`, `model`, and the environment variable named by
`credential_env` in your config. The bundled examples are generic starting
points for broker-owned policy, not declarations for any consumer: each
consumer keeps its own `consumer-compat/v1` manifest in its own repository
and must pass the desired/effective compatibility checks before submitting.
Compatibility is feature-tested and documented in
[docs/openai-compatible.md](docs/openai-compatible.md) — endpoint naming alone
is never trusted.

## API in 60 seconds

Defined by [`openapi.yaml`](openapi.yaml) (OpenAPI 3.1). Representative calls
against a local instance:

```bash
curl -s http://127.0.0.1:8010/health          # process liveness
curl -s http://127.0.0.1:8010/ready           # 200 only when a worker is active
curl -s http://127.0.0.1:8010/metrics         # Prometheus text format

curl -s -X POST http://127.0.0.1:8010/v1/tasks \
  -H "Authorization: Bearer <token>" \
  -H "Idempotency-Key: my-op-key-0001" \
  -H "Content-Type: application/json" \
  -d '{"workload":"default","capability":"instruct",
       "messages":[{"role":"user","content":"Hello"}],
       "response":{"kind":"text"}}'

curl -s http://127.0.0.1:8010/v1/tasks/<task-id> \
  -H "Authorization: Bearer <token>"
curl -s http://127.0.0.1:8010/v1/tasks/<task-id>/result \
  -H "Authorization: Bearer <token>"
curl -s -X POST http://127.0.0.1:8010/v1/tasks/<task-id>/cancel \
  -H "Authorization: Bearer <token>"
```

Result envelopes are `pending`, `succeeded`, `failed` (stable error
class/code, never raw provider output), `cancelled`, or `expired`. Errors are
RFC 7807 `application/problem+json` with stable codes. Details and edge cases
are in [docs/api.md](docs/api.md).

## Stack and layout

- **Go 1.26+**, standard library `net/http`, `log/slog`, `context` — no HTTP
  framework, no ORM. Database access is `pgx/v5` with explicit SQL;
  migrations are Goose with embedded SQL.
- **PostgreSQL 17** as the source of truth, with separate migration-owner,
  provisioner, and runtime roles.
- **Single static binary + minimal non-root container image**
  (`CGO_ENABLED=0`, `Dockerfile`).

```text
cmd/llm-broker/            # single binary: api | worker | migrate | policy | client | version
internal/api/              # HTTP surface (mirrors openapi.yaml)
internal/worker/           # claim → infer → persist loop, lease handling
internal/scheduler/        # broker-owned ordering, affinity batches
internal/store/            # durable tasks, attempts, retention (pgx, explicit SQL)
internal/backend/          # Adapter interface + fake + openai_compatible
internal/policy/           # config v2 validation, apply, topology digest
migrations/                # forward-only Goose migrations
contracts/consumer-compat/ # consumer compatibility manifests (v1)
examples/                  # fake.json, openai-compatible*.json starting points
deploy/compose/            # disposable local stack wiring
docs/                      # quickstart, api, scheduler, operations, security
openapi.yaml               # normative HTTP contract (OpenAPI 3.1)
```

Contributing conventions (adapter development, testing with `-race`,
`make check`) are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Operations

- Migrations run only when you run them (`llm-broker migrate up` with the
  migration role). The API and worker refuse to start until the configured
  policy is applied (`policy validate` → `diff` → `apply` → `status`).
- Retention is per workload policy (defaults: payload 1h, result 7d,
  metadata/idempotency 30d) with a restart-safe sweep every 10 minutes.
- Backups: `pg_dump -Fc`, restore into same-major PostgreSQL, verify
  `migrate status` and `/ready` before resuming traffic. Rehearse with
  `scripts/restore-rehearsal.sh`.
- Bind the API to loopback unless a TLS-terminating proxy is in front; never
  expose the model runtime publicly.

Full procedures are in [docs/operations.md](docs/operations.md) and
[docs/database.md](docs/database.md).

## What it is not

- Not a model server, proxy, or gateway — backends are compile-time Go
  adapters behind a documented interface. Runtime-loaded plugins are not
  supported in v1.
- Not a multi-worker parallel engine — v1 runs exactly one inference at a
  time by design.
- Not a prompt framework — no LangChain/agents/RAG; clients own their
  prompts, the broker owns scheduling and routing.

## Compatibility

LLM Broker v1 supports tested OpenAI-compatible Chat Completions endpoints.
Implementations vary, so compatibility is feature-tested rather than inferred
from endpoint naming. Consumer-side expectations live in
[contracts/consumer-compat/v1](contracts/consumer-compat/v1) and each
consumer's own manifest — never in this core.

## License and security

Apache License 2.0. See [LICENSE](LICENSE).

See [SECURITY.md](SECURITY.md) for supported versions, disclosure policy, and
deployment guidance.
