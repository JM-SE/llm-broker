# Security And Privacy Model

This document states the data inventory, trust boundaries, threat model, and
accepted residual risks of the broker as a generic product. Deployment
profiles (integration tracks) document the concrete runtime assumptions on
top of this model.

## Data Inventory

| Data | Where | Retention | Sensitivity |
| --- | --- | --- | --- |
| Client credentials | clients table, SHA-256 digest only | until revoked/disabled | Secret |
| Idempotency key | tasks table | 30 days (metadata window) | Sensitive |
| Request hash | tasks table | 30 days (survives payload purge) | Non-sensitive fingerprint |
| Task payload (messages/schema/trace) | task_payloads table | 1 hour after terminal | High (prompts) |
| Inference result (content/usage) | task_results table | 7 days after terminal | High (outputs) |
| Attempt metadata (class/code only) | task_attempts table | 30 days | Non-sensitive |
| Worker heartbeats | worker_heartbeats table | pruned by cleanup | Non-sensitive |
| Reasoning content | nowhere | never persisted, logged, or returned | Forbidden |

The database is a sensitive store: backups and the machine holding it must
be protected accordingly. No prompt, result, schema, reasoning content, or
credential ever appears in logs or metrics.

## Trust Boundaries

1. **Clients -> API**: authenticated with bearer credentials (digest
   verified in constant time). Clients can only submit workloads granted to
   them and capabilities configured by the operator. Clients cannot select
   backends, models, priorities, or inference parameters outside the
   normalized vocabulary.
2. **API -> worker**: same process host, no separate channel. The API never
   schedules or executes inference.
3. **Worker -> backend**: the only component that calls inference backends.
   Backend credentials come from environment variables referenced in
   configuration; the credential is never exposed through the API,
   configuration output, or logs.
4. **Operator -> system**: trusted. Operators configure backends, workloads,
   and capabilities; provision credentials; and resolve ambiguous
   executions.

## Unsupported Configurations

- Direct access to inference backends by clients (bypasses scheduling,
  admission, retries, retention, and the one-inference invariant).
- Running the API or worker with database credentials that can alter the
  schema (migration role must stay offline).
- Exposing `/metrics` on a public interface: it is operator-only.

## Threat Model Summary

| Threat | Mitigation |
| --- | --- |
| Credential theft (DB leak) | only SHA-256 digests stored; plaintext tokens are written only to mode-0600 files |
| Token replay | bearer tokens; disable/revoke takes effect immediately |
| Client impersonation | constant-time verification; disabled == invalid (no side channel) |
| Cross-client task access | every query scoped by client in SQL; 404 for foreign tasks |
| Idempotency abuse | per-client keys; replay returns original; conflict on reuse |
| Resource exhaustion (body/schema) | hard byte/depth/count limits at decode time |
| Admission abuse | per-client active-task limits per workload |
| Prompt/result exfiltration via logs/metrics | sanitized logging (route labels only) and bounded metric labels; canary tests |
| Schema injection | JSON Schema treated as data; no network/filesystem loads; remote refs rejected |
| Provider output injection | outputs validated against response contract before persistence |
| Unbounded queue growth | bounded retention job; terminal data purged by policy |
| Ambiguous backend outcome | indeterminate class blocks all inference until operator resolution; no auto-retry |
| Dependency vulnerabilities | govulncheck in CI; zero critical findings policy |
| DoS via /metrics | loopback default listener; operator opt-in |

## Accepted Residual Risks

- The API binds loopback by default; a public bind is possible but places
  TLS and network protection on the operator.
- No per-IP rate limiting: admission is per-credential/workload, matching
  the trusted-clients model.
- A compromised worker process could read backend credentials from its
  environment; run worker and API under separate least-privilege accounts
  in deployment.
- Expired task leases indicate recovery work, never proof that inference
  stopped; operators must use `llm-broker worker status`/`resolve` with
  backend idle evidence before requeueing.

## Reporting

See SECURITY.md for private vulnerability disclosure.
