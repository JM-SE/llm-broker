# Contributing

## Development

LLM Broker uses Go 1.26+ with standard library tooling and a minimal set of
direct dependencies.

```bash
make build        # build the binary
make test         # run all tests
make test-race    # run tests with race detector
make vet          # run go vet
make fmt          # format source
make check        # run all checks
```

## Conventions

- Standard library `net/http`, `log/slog`, `context`, and `crypto` are
  preferred. No HTTP framework or ORM is used.
- Database access uses `pgx/v5` with explicit SQL. No query builder or ORM.
- Migrations use Goose with embedded SQL files. See `migrations/`.
- Backend adapters implement the documented `backend.Adapter` interface.
- Commit messages are concise and follow the existing project style.
- Sensitive values (credentials, paths, model identifiers, keys) may not
  appear in source code, tests, commit messages, or CI output.

## Adding A Backend Adapter

1. Implement the `backend.Adapter` interface.
2. Register the adapter in `internal/backend/registry.go`.
3. Add a conformant test suite using `internal/backend/adaptertest/`.
4. Document protocol compatibility in `docs/backends/`.

Adapters are compiled into the binary. Runtime-loaded plugins are not
supported in v1.

## Testing

- Pure unit tests validate state, policy, scheduling, errors, and limits.
- PostgreSQL integration tests require a disposable database.
- Adapter conformance tests use `httptest` servers, not real models.
- Run `make test-race` before submitting changes.

## License

Apache License 2.0. See [LICENSE](LICENSE).
