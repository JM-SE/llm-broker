# LLM Broker API

The client-facing HTTP surface is defined by `openapi.yaml` (OpenAPI 3.1).
This page documents operator setup and representative `curl` usage against an
isolated instance. The API never schedules or executes inference: tasks are
durable records that a worker (later phases) picks up.

## Operator setup

1. Apply migrations with a migration role:

   ```sh
   llm-broker migrate up
   ```

2. Validate and apply configuration v2 policy with the provisioner role:

   ```sh
   llm-broker policy validate [config]
   llm-broker policy diff [config]   # exit 2 means configured drift
   llm-broker policy apply [config]
   llm-broker policy status [config]
   ```

   Apply is transactional and stores a secret-free topology digest. API and
   worker refuse startup until every locally configured workload is applied.

3. Provision a client credential. The token is written only to an exclusive
   mode-0600 file and is never displayed:

   ```sh
   llm-broker client create ops --credential-file /secure/path/ops.credential
   llm-broker client grant ops default   # grant a workload
   llm-broker client ungrant ops default # remove only that grant
   llm-broker client disable ops        # reject the credential
   llm-broker client revoke ops --credential-file /secure/path/ops.credential.new
   ```

   Client lifecycle and grant commands require the provisioner URL environment
   configured as `database.provisioner_url_env`; they never fall back to the
   runtime credential.

4. Start the API (loopback by default):

   ```sh
   llm-broker api
   ```

## Liveness and readiness

```sh
curl -s http://127.0.0.1:8010/health   # {"status":"ok"} while the process is alive
curl -s http://127.0.0.1:8010/ready    # {"ready":true} only when the database is reachable and migrated
```

## Creating a task

```sh
curl -s -X POST http://127.0.0.1:8010/v1/tasks \
  -H "Authorization: Bearer <token>" \
  -H "Idempotency-Key: my-op-key-0001" \
  -H "Content-Type: application/json" \
  -d '{
    "workload": "default",
    "capability": "instruct",
    "messages": [{"role": "user", "content": "Hello"}],
    "generation": {"max_tokens": 256},
    "response": {"kind": "text"},
    "trace": {"job_id": "42"}
  }'
```

- `201` with a `Location` header: accepted; the task is `queued`.
- Replaying the same key with the same normalized request returns the same
  task (`201`); with a different request it returns `409
  idempotency_conflict`. Keys are scoped per credential.
- `401` invalid credential, `403` workload unknown/not granted, `422`
  contract violation, `429` admission limit, `400` malformed body/headers.

## Optional bounded synchronous wait

```sh
curl -s -X POST http://127.0.0.1:8010/v1/tasks \
  -H "Authorization: Bearer <token>" \
  -H "Idempotency-Key: my-op-key-0002" \
  -H "Prefer: wait=10" \
  -H "Content-Type: application/json" \
  -d '{"workload":"default","capability":"instruct",
       "messages":[{"role":"user","content":"Hello"}],
       "response":{"kind":"text"}}'
```

- `200` with the result envelope if the task reaches a terminal state within
  the window.
- `202` with `Location` and `Retry-After` when the window elapses first; the
  task stays queued. A client disconnect during the wait never cancels work.

## Reading tasks and results

```sh
curl -s http://127.0.0.1:8010/v1/tasks/<task_id> -H "Authorization: Bearer <token>"
curl -s http://127.0.0.1:8010/v1/tasks/<task_id>/result -H "Authorization: Bearer <token>"
```

The result envelope status is `pending`, `succeeded` (with content and usage),
`failed` (with a stable error class/code, never raw provider output),
`cancelled`, or `expired` (result retained away). Reads are scoped: another
credential sees `404`.

## Cancelling

```sh
curl -s -X POST http://127.0.0.1:8010/v1/tasks/<task_id>/cancel \
  -H "Authorization: Bearer <token>"
```

Queued tasks cancel immediately. Running tasks record durable cancel intent
(the worker discards the output after the current inference). Already
cancelled tasks answer idempotently; succeeded/failed tasks answer `409`.

## Operational notes

- Errors are RFC 7807 `application/problem+json` with stable codes
  (`malformed_request`, `unprocessable`, `unauthorized`, `forbidden`,
  `not_found`, `conflict`, `idempotency_conflict`, `admission_limited`,
  `unavailable`, `internal_error`).
- Logs carry only method, path, status, duration, and `X-Request-Id`; they
  never contain credentials, idempotency keys, or message content.
- The API binds `api.listen` from the configuration (loopback by default) and
  never migrates the schema on startup.
