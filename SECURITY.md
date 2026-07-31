# Security Policy

## Supported Versions

LLM Broker v1 is the current stable series. Security patches are provided for
the latest minor release.

## Reporting a Vulnerability

Do not open a public issue. Report suspected security vulnerabilities
privately to the maintainers.

Include:

- Affected version or commit.
- Steps to reproduce.
- Impact assessment.

You will receive an acknowledgement within 5 business days and a status
update within 30 days.

## Deployment Guidance

- Bind the API listener to loopback or a private network unless a
  TLS-terminating reverse proxy is explicitly configured.
- Never expose the backend runtime (llama-swap, llama-server, or equivalent)
  on a public interface.
- Provision client credentials with least privilege and rotate them
  according to your operational policy.
- Run database migrations explicitly. The broker API and worker never
  auto-migrate at startup.
- Back up the dedicated broker database before and after migrations.
  Treat all database contents as sensitive.
- Set `captureBuffer: 0` and `sendLoadingState: false` on proxy/runtime
  layers that buffer request or response data in memory.
- Monitor `/ready` endpoints. Do not send traffic to a worker that reports
  not-ready after an indeterminate backend outcome.

## Trust Boundaries

- The broker cannot prevent another process from calling the model runtime
  directly. Access to the runtime must be enforced at the network or OS
  level.
- Client credentials are stored as SHA-256 digests. If the database is
  compromised, rotate all credentials.
- An expired worker lease does not prove that backend generation has
  stopped. Indeterminate recovery requires explicit operator action
  or a trusted runtime controller.
