package postgres

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
)

// SubmissionParams is the store boundary for a normalized API submission.
// CredentialDigestProof is a digest of the presented credential; a raw token
// is intentionally not part of this DTO.
type SubmissionParams struct {
	ClientID              int64
	CredentialDigestProof [32]byte
	WorkloadSlug          string
	Capability            string
	IdempotencyKey        string
	RequestHash           []byte
	Payload               []byte
	Submission            *domain.TaskSubmission
	LocalMembership       bool
	CapabilityKnown       bool
	LocalTopologyDigest   [32]byte
	ConfiguredTopologies  []ConfiguredWorkloadTopology
}

// SubmissionResult is the authorized task and the durable policy used for the
// decision. Created is false for an idempotent replay.
type SubmissionResult struct {
	Task    Task
	Policy  WorkloadPolicy
	Created bool
}

// These sentinels are the only authorization outcomes exposed to API code.
// In particular, forbidden deliberately makes no distinction between a
// missing workload, grant, allowlist row, capability, or local membership.
var (
	ErrSubmissionCredentialInvalid = errors.New("submission credential invalid")
	ErrSubmissionForbidden         = errors.New("submission forbidden")
	ErrSubmissionGeneration        = errors.New("submission generation rejected")
	ErrSubmissionAdmission         = errors.New("submission admission limited")
	ErrSubmissionTopology          = errors.New("submission topology unavailable")
)

// SubmissionGenerationError carries only the bounded policy-validation detail
// suitable for the existing 422 response. It never contains request bodies or
// credentials.
type SubmissionGenerationError struct {
	Detail string
}

func (e *SubmissionGenerationError) Error() string { return e.Detail }
func (e *SubmissionGenerationError) Unwrap() error { return ErrSubmissionGeneration }

// SubmitTask performs the H00 submission linearization. The transaction is
// READ COMMITTED and acquires client -> policy -> grant/admission locks before
// any authorization-table read. The grant/admission lock also serializes the
// active count and insertion for this client/workload pair.
func (s *Store) SubmitTask(ctx context.Context, p SubmissionParams) (SubmissionResult, error) {
	policyKey, err := policy.PolicyLockKey(p.WorkloadSlug)
	if err != nil {
		return SubmissionResult{}, err
	}
	clientKey, err := policy.ClientLockKey(p.ClientID)
	if err != nil {
		return SubmissionResult{}, err
	}
	grantKey, err := policy.GrantAdmissionLockKey(p.ClientID, p.WorkloadSlug)
	if err != nil {
		return SubmissionResult{}, err
	}

	var result SubmissionResult
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("begin submission transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, key := range []int64{clientKey, policyKey, grantKey} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return SubmissionResult{}, fmt.Errorf("acquire submission lock: %w", err)
		}
	}

	client, err := readSubmissionClient(ctx, tx, p.ClientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return SubmissionResult{}, ErrSubmissionCredentialInvalid
		}
		return SubmissionResult{}, fmt.Errorf("read submission client: %w", err)
	}
	if !client.enabled || len(client.digest) != 32 ||
		subtle.ConstantTimeCompare(client.digest, p.CredentialDigestProof[:]) != 1 {
		return SubmissionResult{}, ErrSubmissionCredentialInvalid
	}
	if err := checkConfiguredTopologyTx(ctx, tx, p.ConfiguredTopologies); err != nil {
		if errors.Is(err, ErrSubmissionTopology) {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, fmt.Errorf("read configured topology: %w", err)
	}
	// Topology coherence is an operational serving boundary. Check it before
	// request-local authorization facts so incoherence cannot become an
	// ordinary client 403 for a different workload or capability.
	if !p.LocalMembership || !p.CapabilityKnown {
		return SubmissionResult{}, ErrSubmissionForbidden
	}

	durable, err := readSubmissionPolicy(ctx, tx, p.WorkloadSlug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// A configured local workload with no durable projection is an
			// operational coherence failure, not a client authorization denial.
			return SubmissionResult{}, ErrSubmissionTopology
		}
		return SubmissionResult{}, fmt.Errorf("read submission policy: %w", err)
	}
	if len(durable.TopologyDigest) != 32 ||
		!bytes.Equal(durable.TopologyDigest, p.LocalTopologyDigest[:]) {
		return SubmissionResult{}, ErrSubmissionTopology
	}

	granted, err := submissionGrantExists(ctx, tx, p.ClientID, durable.ID)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("read submission grant: %w", err)
	}
	allowed := granted &&
		containsCapability(durable.AllowedCapabilities, p.Capability)
	if !allowed {
		return SubmissionResult{}, ErrSubmissionForbidden
	}

	// Authorization is intentionally before idempotency. A revoked request may
	// neither replay nor conflict, and a denied new key must create no row.
	existing, err := s.getTaskByKey(ctx, tx, p.ClientID, p.IdempotencyKey)
	if err == nil {
		if !equalBytes(existing.RequestHash, p.RequestHash) {
			return SubmissionResult{}, fmt.Errorf("%w: idempotency key reused with a different request", ErrConflict)
		}
		result = SubmissionResult{Task: existing, Policy: durable, Created: false}
		if err := tx.Commit(ctx); err != nil {
			return SubmissionResult{}, fmt.Errorf("commit submission replay: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return SubmissionResult{}, fmt.Errorf("read idempotency key: %w", err)
	}

	if p.Submission != nil {
		if err := policy.ValidateGeneration(p.Submission.Generation, durable.AllowedGeneration, durable.MaxOutputTokens); err != nil {
			return SubmissionResult{}, &SubmissionGenerationError{Detail: err.Error()}
		}
	}

	var active int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM tasks
		WHERE client_id = $1 AND workload_id = $2
		  AND state IN ('queued', 'running')`, p.ClientID, durable.ID).Scan(&active); err != nil {
		return SubmissionResult{}, fmt.Errorf("count active submissions: %w", err)
	}
	if active >= int64(durable.MaxActiveTasks) {
		return SubmissionResult{}, ErrSubmissionAdmission
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO tasks (client_id, workload_id, capability, idempotency_key, request_hash)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+taskColumns,
		p.ClientID, durable.ID, p.Capability, p.IdempotencyKey, p.RequestHash)
	task, err := scanTask(row)
	if err != nil {
		return SubmissionResult{}, fmt.Errorf("insert submission task: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO task_payloads (task_id, payload) VALUES ($1, $2)`,
		task.ID, string(p.Payload)); err != nil {
		return SubmissionResult{}, fmt.Errorf("insert submission payload: %w", err)
	}

	result = SubmissionResult{Task: task, Policy: durable, Created: true}
	if err := tx.Commit(ctx); err != nil {
		return SubmissionResult{}, fmt.Errorf("commit submission: %w", err)
	}
	return result, nil
}

type submissionClient struct {
	digest  []byte
	enabled bool
}

func readSubmissionClient(ctx context.Context, tx pgx.Tx, clientID int64) (submissionClient, error) {
	var c submissionClient
	err := tx.QueryRow(ctx,
		`SELECT credential_digest, enabled FROM clients WHERE id = $1`, clientID).
		Scan(&c.digest, &c.enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return submissionClient{}, ErrNotFound
	}
	return c, err
}

// checkConfiguredTopologyTx verifies the complete process-local workload
// membership proof in one transaction statement. A request cannot admit work
// for one coherent workload while another locally configured workload is
// missing, unprovisioned, or routed through a different topology.
func checkConfiguredTopologyTx(ctx context.Context, tx pgx.Tx, configured []ConfiguredWorkloadTopology) error {
	if len(configured) == 0 {
		// A submission is only safe when the caller proves the complete local
		// topology. Allowing an empty proof would let a future caller bypass the
		// global serving fence and admit work after another configured workload
		// drifted.
		return ErrSubmissionTopology
	}

	expected := make(map[string][32]byte, len(configured))
	slugs := make([]string, 0, len(configured))
	for _, item := range configured {
		if prior, ok := expected[item.Slug]; ok {
			if prior != item.Digest {
				return ErrSubmissionTopology
			}
			continue
		}
		expected[item.Slug] = item.Digest
		slugs = append(slugs, item.Slug)
	}

	rows, err := tx.Query(ctx,
		`SELECT name, topology_digest FROM workload_policies WHERE name = ANY($1::text[])`,
		slugs)
	if err != nil {
		return fmt.Errorf("query configured workload topology: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(expected))
	for rows.Next() {
		var slug string
		var digest []byte
		if err := rows.Scan(&slug, &digest); err != nil {
			return fmt.Errorf("scan configured workload topology: %w", err)
		}
		want, ok := expected[slug]
		if !ok || len(digest) != len(want) || !bytes.Equal(digest, want[:]) {
			return ErrSubmissionTopology
		}
		seen[slug] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read configured workload topology: %w", err)
	}
	if len(seen) != len(expected) {
		return ErrSubmissionTopology
	}
	return nil
}

func submissionGrantExists(ctx context.Context, tx pgx.Tx, clientID, workloadID int64) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM client_workload_grants
		WHERE client_id = $1 AND workload_id = $2`, clientID, workloadID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func containsCapability(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readSubmissionPolicy(ctx context.Context, tx pgx.Tx, slug string) (WorkloadPolicy, error) {
	var p WorkloadPolicy
	err := tx.QueryRow(ctx, `SELECT id,name,priority_class,max_active_tasks,max_attempts,
		retry_delays_seconds,max_output_tokens,allowed_generation,required_features,
		workload_timeout_seconds,payload_retention_seconds,result_retention_seconds,
		metadata_retention_seconds,topology_digest,
		COALESCE((SELECT array_agg(capability ORDER BY capability)
			FROM workload_policy_capabilities wpc
			WHERE wpc.workload_id=workload_policies.id), '{}'::text[]),
		created_at,updated_at
		FROM workload_policies WHERE name=$1`, slug).Scan(
		&p.ID, &p.Name, &p.PriorityClass, &p.MaxActiveTasks, &p.MaxAttempts,
		&p.RetryDelaysSeconds, &p.MaxOutputTokens, &p.AllowedGeneration,
		&p.RequiredFeatures, &p.WorkloadTimeoutSeconds, &p.PayloadRetentionSeconds,
		&p.ResultRetentionSeconds, &p.MetadataRetentionSeconds, &p.TopologyDigest,
		&p.AllowedCapabilities, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkloadPolicy{}, ErrNotFound
	}
	return p, err
}
