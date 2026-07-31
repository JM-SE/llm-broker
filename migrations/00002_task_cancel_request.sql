-- +goose Up
-- Cancel intent for running tasks: the API records that the client asked to
-- cancel; the worker honors it after the current inference completes and
-- discards the output. Queued tasks cancel immediately and never set this
-- column, hence the invariant: only running tasks may carry cancel intent.
ALTER TABLE tasks ADD COLUMN cancel_requested_at timestamptz;
ALTER TABLE tasks
    ADD CONSTRAINT tasks_cancel_requested_only_running
    CHECK (cancel_requested_at IS NULL OR state = 'running');

-- Credential lookup identifier (auth phase): the short prefix derived from a
-- bearer credential, used for constant-time verification lookups. The full
-- token is never stored.
ALTER TABLE clients ADD COLUMN credential_identifier text NOT NULL
    CHECK (length(credential_identifier) = 12);
CREATE INDEX clients_credential_identifier_idx ON clients (credential_identifier);

-- +goose Down
DROP INDEX clients_credential_identifier_idx;
ALTER TABLE clients DROP COLUMN credential_identifier;
ALTER TABLE tasks DROP CONSTRAINT tasks_cancel_requested_only_running;
ALTER TABLE tasks DROP COLUMN cancel_requested_at;
