# Quick Start (fake mode)

This guide brings up the full stack with **no contact with a real model**:
the built-in fake backend answers deterministically. You need Docker with
Compose.

## 1. Start the stack

```bash
git clone <your-copy-of-the-repo> && cd llm-broker
cp compose.example.yaml compose.yaml
export LLM_BROKER_DB_PASSWORD="$(openssl rand -hex 24)"
docker compose build
docker compose run --rm migrate migrate up
docker compose exec -T db psql -U llm_broker -d llm_broker < deploy/compose/seed.sql
docker compose up -d
```

The API listens on `http://127.0.0.1:8010` (loopback only).

## 2. Provision a client credential

```bash
docker compose exec api llm-broker client create demo
```

The output shows a bearer token **exactly once**; store it. Grant the
workload:

```bash
docker compose exec api llm-broker client grant demo default
```

## 3. Submit a task and wait

```bash
TOKEN="lbk_..."
curl -sS -X POST http://127.0.0.1:8010/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
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
  -H "Authorization: Bearer $TOKEN"
curl -sS http://127.0.0.1:8010/v1/tasks/<task-id>/result \
  -H "Authorization: Bearer $TOKEN"
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
  -H "Authorization: Bearer $TOKEN"
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
