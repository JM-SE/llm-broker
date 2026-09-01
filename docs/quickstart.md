# Quick Start (fake mode)

This guide brings up the full stack with **no contact with a real model**:
the built-in fake backend answers deterministically. You need Docker with
Compose.

## 1. Start the stack

```bash
git clone <your-copy-of-the-repo> && cd llm-broker
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

The API listens on `http://127.0.0.1:8010` (loopback only).

## 2. Provision a client credential

```bash
credential_dir="$(mktemp -d)"
chmod 700 "$credential_dir"
docker compose run --rm --user "$(id -u):$(id -g)" \
  -v "$credential_dir:/credentials:rw" policy-apply client create demo \
  --credential-file /credentials/demo.credential
chmod 600 "$credential_dir/demo.credential"
```

The credential is written to a host-mounted exclusive mode-0600 file; it is
never printed. Create a temporary curl configuration so the bearer value is
not expanded into a curl command argument:

```bash
curl_cfg="$(mktemp)"
chmod 600 "$curl_cfg"
printf 'header = "Authorization: Bearer %s"\n' \
  "$(< "$credential_dir/demo.credential")" >"$curl_cfg"
trap 'rm -f "$curl_cfg"' EXIT
```

Grant the workload:

```bash
docker compose run --rm policy-apply client grant demo default
```

Remove the temporary credential directory after the client has been securely
stored elsewhere:

```bash
rm -rf "$credential_dir"
```

## 3. Submit a task and wait

```bash
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
```

Because the worker and fake backend are running, the response is a terminal
envelope (`"status": "succeeded"`) with the fake content. Task and result
can also be polled:

```bash
curl -sS http://127.0.0.1:8010/v1/tasks/<task-id> \
  --config "$curl_cfg"
curl -sS http://127.0.0.1:8010/v1/tasks/<task-id>/result \
  --config "$curl_cfg"
```

## 4. Operations

```bash
curl -sS http://127.0.0.1:8010/health          # process liveness
curl -sS http://127.0.0.1:8010/ready           # 200 only when a worker is active
curl -sS http://127.0.0.1:8010/metrics         # Prometheus (metrics_enabled)
docker compose logs -f api worker
```

Cancel a queued task:

```bash
curl -sS -X POST http://127.0.0.1:8010/v1/tasks/<task-id>/cancel \
  --config "$curl_cfg"
```

## 5. Stop and reset

```bash
docker compose down -v     # destroys the database volume
```

## What is guaranteed

- Exactly **one** inference runs at a time (global invariant), and it is
  always scheduled by the worker — the API never infers.
- Clients never select models, backends, or priorities.
- Migrations run only when you run them.
- Configuration is version 2: every workload declares an explicit
  `allowed_capabilities` list (empty means deny-all) and
  `workload_timeout_seconds` from 1 through 3600. Apply policy after migration
  and before starting API/worker; hand-seeded policy SQL is unsupported.
- The disposable Compose stack creates separate migration-owner, provisioner,
  and runtime database roles. API/worker receive only the runtime credential;
  policy commands receive only the provisioner credential.
- Client credentials are written to an explicitly selected mode-0600 file and
  are never printed by the CLI.

## H00 acceptance evidence

The closure gate is executed only from an immutable candidate commit. The
external evidence bundle records the commit/tree identity, three database-role
probes, required `make check`, focused acceptance output, Compose fake-mode
rollout, restore rehearsal, cleanup inventory, and sanitized scans. Never use
real model credentials or a live application database for this flow; keep
candidate evidence under the registered external AI project workspace.
