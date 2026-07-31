package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Client is the identity row. credential_digest holds a strong one-way hash of
// the client's bearer token; plaintext credentials never reach the database.
type Client struct {
	ID               int64
	Name             string
	CredentialDigest []byte
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CreateClientParams is the input for client provisioning.
type CreateClientParams struct {
	Name             string
	CredentialDigest []byte
	Enabled          bool
}

// CreateClient inserts a client. A duplicate name yields ErrConflict.
func (s *Store) CreateClient(ctx context.Context, p CreateClientParams) (Client, error) {
	var c Client
	err := s.pool.QueryRow(ctx, `
		INSERT INTO clients (name, credential_digest, enabled)
		VALUES ($1, $2, $3)
		RETURNING id, name, credential_digest, enabled, created_at, updated_at`,
		p.Name, p.CredentialDigest, p.Enabled).Scan(
		&c.ID, &c.Name, &c.CredentialDigest, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if isUniqueViolation(err) {
		return Client{}, fmt.Errorf("%w: client %q already exists", ErrConflict, p.Name)
	}
	return c, err
}

// GetClientByName returns a client by name.
func (s *Store) GetClientByName(ctx context.Context, name string) (Client, error) {
	var c Client
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, credential_digest, enabled, created_at, updated_at
		FROM clients WHERE name = $1`, name).Scan(
		&c.ID, &c.Name, &c.CredentialDigest, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// GetClientByID returns a client by ID.
func (s *Store) GetClientByID(ctx context.Context, id int64) (Client, error) {
	var c Client
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, credential_digest, enabled, created_at, updated_at
		FROM clients WHERE id = $1`, id).Scan(
		&c.ID, &c.Name, &c.CredentialDigest, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// SetClientEnabled toggles a client's enabled state.
func (s *Store) SetClientEnabled(ctx context.Context, clientID int64, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE clients SET enabled = $2, updated_at = now() WHERE id = $1`,
		clientID, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkloadPolicy is the operator-defined policy row synced from configuration.
type WorkloadPolicy struct {
	ID                       int64
	Name                     string
	PriorityClass            string
	MaxActiveTasks           int
	MaxAttempts              int
	RetryDelaysSeconds       []int
	MaxOutputTokens          int
	AllowedGeneration        []byte
	RequiredFeatures         []string
	PayloadRetentionSeconds  int64
	ResultRetentionSeconds   int64
	MetadataRetentionSeconds int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// UpsertWorkloadPolicy inserts or replaces a workload policy by name.
func (s *Store) UpsertWorkloadPolicy(ctx context.Context, p WorkloadPolicy) (WorkloadPolicy, error) {
	var out WorkloadPolicy
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workload_policies (
			name, priority_class, max_active_tasks, max_attempts, retry_delays_seconds,
			max_output_tokens, allowed_generation, required_features,
			payload_retention_seconds, result_retention_seconds, metadata_retention_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11)
		ON CONFLICT (name) DO UPDATE SET
			priority_class = EXCLUDED.priority_class,
			max_active_tasks = EXCLUDED.max_active_tasks,
			max_attempts = EXCLUDED.max_attempts,
			retry_delays_seconds = EXCLUDED.retry_delays_seconds,
			max_output_tokens = EXCLUDED.max_output_tokens,
			allowed_generation = EXCLUDED.allowed_generation,
			required_features = EXCLUDED.required_features,
			payload_retention_seconds = EXCLUDED.payload_retention_seconds,
			result_retention_seconds = EXCLUDED.result_retention_seconds,
			metadata_retention_seconds = EXCLUDED.metadata_retention_seconds,
			updated_at = now()
		RETURNING id, name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, payload_retention_seconds, result_retention_seconds,
			metadata_retention_seconds, created_at, updated_at`,
		p.Name, p.PriorityClass, p.MaxActiveTasks, p.MaxAttempts,
		p.RetryDelaysSeconds, p.MaxOutputTokens, string(p.AllowedGeneration),
		p.RequiredFeatures, p.PayloadRetentionSeconds, p.ResultRetentionSeconds,
		p.MetadataRetentionSeconds).Scan(
		&out.ID, &out.Name, &out.PriorityClass, &out.MaxActiveTasks, &out.MaxAttempts,
		&out.RetryDelaysSeconds, &out.MaxOutputTokens, &out.AllowedGeneration,
		&out.RequiredFeatures, &out.PayloadRetentionSeconds, &out.ResultRetentionSeconds,
		&out.MetadataRetentionSeconds, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// GetWorkloadPolicyByName returns a workload policy by name.
func (s *Store) GetWorkloadPolicyByName(ctx context.Context, name string) (WorkloadPolicy, error) {
	var p WorkloadPolicy
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, priority_class, max_active_tasks, max_attempts,
			retry_delays_seconds, max_output_tokens, allowed_generation,
			required_features, payload_retention_seconds, result_retention_seconds,
			metadata_retention_seconds, created_at, updated_at
		FROM workload_policies WHERE name = $1`, name).Scan(
		&p.ID, &p.Name, &p.PriorityClass, &p.MaxActiveTasks, &p.MaxAttempts,
		&p.RetryDelaysSeconds, &p.MaxOutputTokens, &p.AllowedGeneration,
		&p.RequiredFeatures, &p.PayloadRetentionSeconds, &p.ResultRetentionSeconds,
		&p.MetadataRetentionSeconds, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkloadPolicy{}, ErrNotFound
	}
	return p, err
}

// GrantWorkload grants a workload to a client (idempotent).
func (s *Store) GrantWorkload(ctx context.Context, clientID, workloadID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO client_workload_grants (client_id, workload_id)
		VALUES ($1, $2)
		ON CONFLICT (client_id, workload_id) DO NOTHING`,
		clientID, workloadID)
	return err
}

// RevokeWorkload removes a workload grant.
func (s *Store) RevokeWorkload(ctx context.Context, clientID, workloadID int64) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM client_workload_grants WHERE client_id = $1 AND workload_id = $2`,
		clientID, workloadID)
	return err
}

// ClientHasWorkload reports whether a client is granted a workload.
func (s *Store) ClientHasWorkload(ctx context.Context, clientID, workloadID int64) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		SELECT 1 FROM client_workload_grants
		WHERE client_id = $1 AND workload_id = $2`,
		clientID, workloadID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation
// (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}
