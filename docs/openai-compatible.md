# Generic OpenAI-Compatible Backends

The `openai_compatible` adapter speaks the chat-completions subset of the
OpenAI HTTP API against any compatible endpoint. Compatibility is
deliberately conservative.

## Request Mapping

The normalized request is mapped to a `POST <base_url>/chat/completions`
body with: `model` (the operator-configured alias), `messages`
(system/user/assistant only), and — when present — `max_tokens`,
`temperature`, `top_p`, `top_k`, `min_p`, `seed`, and `stop`. Provider
parameters that have no normalized equivalent (`frequency_penalty`,
`presence_penalty`, `n`, `stream`, `tools`, `user`) are never sent.

For `response.kind = json_object` or `json_schema`, a
`response_format: {"type": "json_object"}` is sent **only** when the
workload declares the `structured_output` feature for the backend;
otherwise the task fails with `feature_not_declared` before any call.

## Response Handling

- `choices[0].message.content` becomes the result content; `reasoning`
  fields are discarded before anything is stored, logged, or returned.
- `finish_reason` maps to `stop`/`length`/`content_filter`/`unknown`.
- Usage is mapped when present; `total_tokens` falls back to
  prompt+completion.
- Outputs are size-limited (4 MiB) and validated against the response
  contract before persistence.

## Failure Classification (fail-closed)

| Situation | Class |
| --- | --- |
| Connection refused / DNS failure before any byte sent | `transient_unsent` (retries) |
| HTTP 429/5xx | `transient_safe` (retries) |
| HTTP 4xx (other), unexpected 3xx | `backend_rejected` (terminal) |
| Timeout, connection reset mid-body, context cancel | `indeterminate` (blocks all inference until operator resolution) |
| Oversized/malformed body | `output_invalid` (terminal) |

A generic HTTP failure alone is **never** a retry decision: only the
classifications above are. If the endpoint accepts the request and the
connection drops before the response, the outcome is unknown; the broker
stops scheduling and asks the operator to verify backend state before
resolving the task.

## Variability

Feature variability across OpenAI-compatible servers is expected: chat
completions support, parameter strictness, and `response_format` behavior
differ between providers. The adapter never guesses; a server that rejects
a parameter surfaces as `backend_rejected` with a stable code. Test your
endpoint against the conformance expectations in
`docs/backends/adapter-development.md` before production use.
