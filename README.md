# LLM Broker

A durable, single-host inference scheduler for applications that share local
or privately hosted language-model servers.

LLM Broker is independent infrastructure software. It does not depend on any
specific model, runtime, or client application.

## Design

- **Exactly one inference globally.** This is a deliberate product guarantee,
  not a deployment limitation.
- **Durable queue.** Every inference is a committed PostgreSQL task. No work is
  lost on restart.
- **Broker-owned scheduling.** Clients submit workload and capability names.
  The broker resolves priority, aging, model affinity, and backend routing.
- **Generic backend boundary.** Built-in `fake` and `openai_compatible` adapters.
  New protocols use a documented compile-time Go interface.
- **Privacy-aware.** Prompts, results, credentials, and reasoning are never
  logged, persisted in operational tables, or exposed in metrics.

## Quick Start

LLM Broker requires Go 1.26+ and PostgreSQL.

```bash
git clone https://github.com/JM-SE/llm-broker.git
cd llm-broker
make build
```

The default configuration uses a `fake` backend that requires no model runtime:

```bash
make run-dev
```

For an OpenAI-compatible endpoint, provide operator configuration and credentials:

```bash
LLM_BROKER_CONFIG_FILE=examples/openai.json make run-dev
```

See `docs/` for detailed setup, operations, and adapter development guides.

## Compatibility

LLM Broker v1 supports tested OpenAI-compatible Chat Completions endpoints.
OpenAI-compatible implementations vary. Compatibility is feature-tested and
documented rather than inferred solely from endpoint naming.

## License

Apache License 2.0. See [LICENSE](LICENSE).

## Security

See [SECURITY.md](SECURITY.md) for supported versions, disclosure policy,
and deployment guidance.
