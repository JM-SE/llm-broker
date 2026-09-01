package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
)

// Attempt phases are durable execution boundaries. A claimed attempt has not
// crossed the non-preemption boundary; an authorized attempt may have reached
// an adapter and is consequently ambiguous after lease expiry.
const (
	AttemptPhaseClaimed    = "claimed"
	AttemptPhaseAuthorized = "authorized"
	AttemptPhaseSucceeded  = "succeeded"
	AttemptPhaseFailed     = "failed"
)

// PreflightParams contains the immutable claim projection plus the worker's
// local topology proof. The worker never supplies a mutable workload lookup
// result or a credential secret here.
type PreflightParams struct {
	TaskID               uuid.UUID
	Attempt              int
	ClientID             int64
	WorkloadID           int64
	WorkloadSlug         string
	Capability           string
	LocalMembership      bool
	ConfiguredCapability bool
	LocalTopologyDigest  [32]byte
}

// Preflight errors intentionally have only two classes. Policy rejection is a
// durable terminal task outcome; operational failure leaves a fresh claimed
// attempt untouched and therefore safe for lease recovery.
var (
	ErrPreflightPolicyRejected = errors.New("worker preflight policy rejected")
	ErrPreflightOperational    = errors.New("worker preflight operational failure")
)

// PreflightTask reads current authorization state under the shared client ->
// policy -> grant/admission advisory locks. A successful call commits the
// claimed -> authorized attempt transition and returns the durable policy.
// No transaction or advisory lock survives this method, so adapter selection
// and inference run outside the authorization transaction.
func (s *Store) PreflightTask(ctx context.Context, p PreflightParams) (WorkloadPolicy, error) {
	policyKey, err := policy.PolicyLockKey(p.WorkloadSlug)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	clientKey, err := policy.ClientLockKey(p.ClientID)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	grantKey, err := policy.GrantAdmissionLockKey(p.ClientID, p.WorkloadSlug)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, key := range []int64{clientKey, policyKey, grantKey} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return WorkloadPolicy{}, operationalPreflightError(err)
		}
	}

	var taskState domain.TaskState
	var taskAttempts int
	if err := tx.QueryRow(ctx,
		`SELECT state, attempts FROM tasks WHERE id = $1`, p.TaskID).
		Scan(&taskState, &taskAttempts); err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	if taskState != domain.TaskRunning || taskAttempts != p.Attempt {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("task state or attempt changed"))
	}
	if !p.LocalMembership || !p.ConfiguredCapability {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("local workload or capability proof is incoherent"))
	}

	client, err := readSubmissionClient(ctx, tx, p.ClientID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return s.commitPreflightDenial(ctx, tx, p)
		}
		return WorkloadPolicy{}, operationalPreflightError(err)
	}

	durable, err := readSubmissionPolicy(ctx, tx, p.WorkloadSlug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// A configured workload without a durable projection is an
			// operational coherence failure. Do not turn it into a client
			// policy rejection or terminalize the task.
			return WorkloadPolicy{}, operationalPreflightError(err)
		}
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	if durable.ID != p.WorkloadID {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("claimed workload identity changed"))
	}
	if len(durable.TopologyDigest) != 32 ||
		!bytes.Equal(durable.TopologyDigest, p.LocalTopologyDigest[:]) {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("local and durable topology digests differ"))
	}

	granted, err := submissionGrantExists(ctx, tx, p.ClientID, durable.ID)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}

	if !client.enabled || !granted || !containsCapability(durable.AllowedCapabilities, p.Capability) {
		return s.commitPreflightDenial(ctx, tx, p)
	}

	// These process-local topology facts were checked before durable policy
	// denial, so a topology/configuration problem never becomes a client policy
	// rejection.

	tag, err := tx.Exec(ctx, `
		UPDATE task_attempts
		SET state = 'authorized'
	WHERE task_id = $1 AND attempt = $2 AND state = 'claimed'`, p.TaskID, p.Attempt)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	if tag.RowsAffected() != 1 {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("claimed attempt was not available for authorization"))
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	return durable, nil
}

func (s *Store) commitPreflightDenial(ctx context.Context, tx pgx.Tx, p PreflightParams) (WorkloadPolicy, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE task_attempts
		SET state = 'failed', error_class = 'policy_rejected',
		    error_code = 'authorization_revoked', finished_at = now()
		WHERE task_id = $1 AND attempt = $2 AND state = 'claimed'`, p.TaskID, p.Attempt)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	if tag.RowsAffected() != 1 {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("claimed attempt denial update did not match"))
	}
	tag, err = tx.Exec(ctx, `
		UPDATE tasks
		SET state = 'failed', finished_at = now(), started_at = NULL,
		    cancel_requested_at = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND state = 'running' AND attempts = $2`, p.TaskID, p.Attempt)
	if err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	if tag.RowsAffected() != 1 {
		return WorkloadPolicy{}, operationalPreflightError(errors.New("running task denial update did not match"))
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkloadPolicy{}, operationalPreflightError(err)
	}
	return WorkloadPolicy{}, fmt.Errorf("%w: authorization revoked", ErrPreflightPolicyRejected)
}

func operationalPreflightError(cause error) error {
	if cause == nil {
		cause = errors.New("unknown preflight failure")
	}
	return fmt.Errorf("%w: authorization preflight unavailable: %w", ErrPreflightOperational, cause)
}
