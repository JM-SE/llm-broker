package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JM-SE/llm-broker/internal/policy"
)

// Client is the identity row. credential_digest holds a strong one-way hash of
// the client's bearer token; plaintext credentials never reach the database.
// credential_identifier is the short lookup prefix derived from the token.
type Client struct {
	ID                   int64
	Name                 string
	CredentialIdentifier string
	CredentialDigest     []byte
	Enabled              bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// CreateClientParams is the input for client provisioning.
type CreateClientParams struct {
	Name                 string
	CredentialIdentifier string
	CredentialDigest     []byte
	Enabled              bool
}

// CreateClient inserts a client. A duplicate name yields ErrConflict. The
// name lock serializes creation before a numeric client ID exists.
func (s *Store) CreateClient(ctx context.Context, p CreateClientParams) (Client, error) {
	var c Client
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		key, err := policy.ClientNameLockKey(p.Name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return fmt.Errorf("acquire client name lock: %w", err)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO clients (name, credential_identifier, credential_digest, enabled)
			VALUES ($1, $2, $3, $4)
			RETURNING id, name, credential_identifier, credential_digest, enabled, created_at, updated_at`,
			p.Name, p.CredentialIdentifier, p.CredentialDigest, p.Enabled).Scan(
			&c.ID, &c.Name, &c.CredentialIdentifier, &c.CredentialDigest, &c.Enabled,
			&c.CreatedAt, &c.UpdatedAt)
	})
	if isUniqueViolation(err) {
		return Client{}, fmt.Errorf("%w: client %q already exists", ErrConflict, p.Name)
	}
	return c, err
}

const clientColumns = `id, name, credential_identifier, credential_digest, enabled, created_at, updated_at`

func scanClient(row pgx.Row) (Client, error) {
	var c Client
	err := row.Scan(&c.ID, &c.Name, &c.CredentialIdentifier, &c.CredentialDigest,
		&c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// GetClientByName returns a client by name.
func (s *Store) GetClientByName(ctx context.Context, name string) (Client, error) {
	c, err := scanClient(s.pool.QueryRow(ctx,
		`SELECT `+clientColumns+` FROM clients WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// GetClientByID returns a client by ID.
func (s *Store) GetClientByID(ctx context.Context, id int64) (Client, error) {
	c, err := scanClient(s.pool.QueryRow(ctx,
		`SELECT `+clientColumns+` FROM clients WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// GetClientByIdentifier returns a client by credential lookup identifier.
func (s *Store) GetClientByIdentifier(ctx context.Context, identifier string) (Client, error) {
	c, err := scanClient(s.pool.QueryRow(ctx,
		`SELECT `+clientColumns+` FROM clients WHERE credential_identifier = $1`, identifier))
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// RotateClientCredential replaces the credential digest and lookup identifier
// of a client, invalidating the previous token.
func (s *Store) RotateClientCredential(ctx context.Context, clientID int64, identifier string, digest []byte) (Client, error) {
	var c Client
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		key, err := policy.ClientLockKey(clientID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
		UPDATE clients
		SET credential_identifier = $2, credential_digest = $3, updated_at = now()
		WHERE id = $1
		RETURNING `+clientColumns, clientID, identifier, digest).Scan(
			&c.ID, &c.Name, &c.CredentialIdentifier, &c.CredentialDigest, &c.Enabled,
			&c.CreatedAt, &c.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// SetClientEnabled toggles a client's enabled state.
func (s *Store) SetClientEnabled(ctx context.Context, clientID int64, enabled bool) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		key, err := policy.ClientLockKey(clientID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE clients SET enabled = $2, updated_at = now() WHERE id = $1`,
			clientID, enabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GrantWorkloadBySlug grants using the immutable workload identity. The lock
// is acquired from the slug before the workload row is used for mutation.
func (s *Store) GrantWorkloadBySlug(ctx context.Context, clientID int64, slug string) error {
	workloadKey, err := policy.PolicyLockKey(slug)
	if err != nil {
		return err
	}
	grantKey, err := policy.GrantAdmissionLockKey(clientID, slug)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		clientKey, err := policy.ClientLockKey(clientID)
		if err != nil {
			return err
		}
		for _, key := range []int64{clientKey, workloadKey, grantKey} {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				return err
			}
		}
		var workloadID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM workload_policies WHERE name=$1`, slug).Scan(&workloadID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO client_workload_grants (client_id, workload_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, clientID, workloadID)
		return err
	})
}

// RevokeWorkloadBySlug removes only the selected grant.
func (s *Store) RevokeWorkloadBySlug(ctx context.Context, clientID int64, slug string) error {
	workloadKey, err := policy.PolicyLockKey(slug)
	if err != nil {
		return err
	}
	grantKey, err := policy.GrantAdmissionLockKey(clientID, slug)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		clientKey, err := policy.ClientLockKey(clientID)
		if err != nil {
			return err
		}
		for _, key := range []int64{clientKey, workloadKey, grantKey} {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				return err
			}
		}
		var workloadID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM workload_policies WHERE name=$1`, slug).Scan(&workloadID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM client_workload_grants WHERE client_id=$1 AND workload_id=$2`, clientID, workloadID)
		return err
	})
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
