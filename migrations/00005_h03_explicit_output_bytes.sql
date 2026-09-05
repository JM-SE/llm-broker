-- +goose Up
-- H03 adds an independent decoded UTF-8 output-content bound. NULL preserves
-- the v2 legacy max_output_tokens*4 runtime behavior until policy apply opts in.
ALTER TABLE workload_policies
    ADD COLUMN max_output_bytes INTEGER;
ALTER TABLE workload_policies
    ADD CONSTRAINT workload_policies_max_output_bytes_check
    CHECK (max_output_bytes IS NULL OR max_output_bytes BETWEEN 1 AND 262144);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'llm_broker_provisioner') THEN
        GRANT UPDATE (max_output_bytes) ON workload_policies TO llm_broker_provisioner;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE workload_policies DROP CONSTRAINT workload_policies_max_output_bytes_check;
ALTER TABLE workload_policies DROP COLUMN max_output_bytes;
