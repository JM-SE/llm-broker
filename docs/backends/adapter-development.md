# Adapter Development

This document describes how to add a compiled inference adapter to the
broker. Adapters translate the normalized broker request into one provider
protocol and normalize the response back. They are the only product code
that speaks to an inference backend.

## Contract

An adapter is a type implementing `backend.Adapter` (see
`internal/backend/backend.go`):

```go
type Adapter interface {
    Type() string
    Infer(ctx context.Context, req *Request) (*Response, error)
}
```

Rules that are enforced by the conformance suite and by review:

- `Request` contains only normalized domain types: messages (system/user/
  assistant text), the normalized generation vocabulary, and the response
  contract. There is no client identity, priority, model, URL, or provider
  field.
- `Response` contains final content, a normalized finish reason, and usage.
  **Hidden reasoning content is discarded inside the adapter** and must
  never reach the response, an error, a log, or persistence.
- Errors are always `*backend.Error` with a stable class from the broker
  taxonomy:
  - `transient_unsent` — provably nothing was sent (dial failure).
  - `transient_safe` — the backend rejected/errored before processing
    (4xx/5xx/429 semantics).
  - `backend_rejected` — the request was refused.
  - `output_invalid` — the backend responded but the body was malformed,
    oversized, or failed contract validation.
  - `indeterminate` — the request may have been accepted (timeout,
    disconnect mid-response, cancellation). **Never classify these as
    retryable.**
  - `internal` — adapter misconfiguration.
- `Error.Message` is sanitized: no raw provider body, no prompts, no
  credentials.
- Adapters never retry and never touch persistence or scheduling.
- Context cancellation and all transport phase deadlines are honored.
- Construction is inert: no network, no connections.

## Files

A new adapter lives in `internal/backend/<name>/` and typically contains:

- `<name>.go` — the adapter implementation.
- `<name>_test.go` — unit tests against `httptest` fixtures.
- `*_fuzz_test.go` — a fuzz target for the response parser.

### Registration

Registration is compile-time, via `init()`:

```go
func init() {
    backend.RegisterDefault("<type-name>", New)
}
```

The worker binary imports the adapter packages (blank import) so their
`init()` runs; the config `backends.<name>.type` selects the adapter by
this type name.

### Conformance

Run the shared conformance suite from your tests:

```go
conformance.Run(t, func() backend.Adapter { return newAdapter(t) })
```

A new adapter does not ship until every conformance case passes.

## OpenAI-Compatible Adapter

`internal/backend/openaicompat` is the only real protocol adapter in v1. It
implements a narrow, documented subset of Chat Completions:

- Mapped fields: `model` (operator alias), `messages`, `max_tokens`,
  `temperature`, `top_p`, `top_k`, `min_p`, `seed`, `stop`,
  `response_format` (json_object only, and only when the backend declares
  the `structured_output` feature).
- Unsupported parameters are omitted, never guessed.
- Redirects are disabled (`CheckRedirect` returns `ErrUseLastResponse`):
  credentials cannot cross origins.
- Credential comes from the operator-configured environment variable at
  construction; it is never logged or stored.
- Response parsing is bounded (4 MiB output ceiling, 4 KiB error bodies,
  15 s header acknowledgment budget, 5 s dial budget, overall budget from
  the caller context).
- `reasoning_content` is dropped before the response is built (tested and
  fuzzed).
- Classification is conservative: any failure after the request may have
  been accepted is `indeterminate`.

Compatibility claims are scoped to what the httptest fixtures prove; the
docs never claim universal provider compatibility.

## Testing Checklist

- Conformance suite passes.
- Golden request test pins the exact JSON shape (and absence of private or
  unsupported fields).
- Slow server, mid-body disconnect, redirect, connect-refused, oversized
  and malformed bodies, HTTP status classes, and cancellation are covered
  with `httptest` (never a real endpoint).
- Race detector clean; `go test -p 1 ./...` (integration packages share a
  disposable database).
