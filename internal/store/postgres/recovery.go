package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// RecoveryItem describes one running task whose latest attempt needs recovery.
// A claimed phase is definitely unsent and safe to requeue; an authorized
// phase (including a converted legacy attempt) is ambiguous and remains on
// the operator-recovery path.
type RecoveryItem struct {
	Task          Task
	ErrorClass    string
	ErrorCode     string
	AttemptNo     int
	Phase         string
	SafeToRequeue bool
	LeaseExpired  bool
}

// HasUnresolvedIndeterminate reports whether any task is in an ambiguous state
// that blocks all new inference: a latest indeterminate attempt, or an
// expired authorized attempt. Once an indeterminate outcome is recorded the
// execution is over (only its outcome is unknown), so that case does not wait
// for lease expiry. Authorized expiry is conservatively operator-recoverable.
func (s *Store) HasUnresolvedIndeterminate(ctx context.Context) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM tasks t
			JOIN LATERAL (
				SELECT state, error_class
				FROM task_attempts a
				WHERE a.task_id = t.id
				ORDER BY attempt DESC
				LIMIT 1
			) la ON true
			WHERE t.state = 'running'
			  AND ((la.state = 'failed' AND la.error_class = 'indeterminate')
			       OR (la.state = 'authorized' AND t.lease_until <= now()))
			)`).Scan(&exists)
	return exists, err
}

// ListUnresolvedRecovery returns expired claimed and authorized attempts plus
// indeterminate attempts. It intentionally uses the latest attempt only, so a
// prior failed attempt cannot keep a newer safe/authorized attempt hidden.
func (s *Store) ListUnresolvedRecovery(ctx context.Context) ([]RecoveryItem, error) {
	q := `SELECT t.id, t.client_id, t.workload_id, t.capability, t.idempotency_key,
		t.request_hash, t.state, t.attempts, t.lease_until, t.started_at,
		t.finished_at, t.cancel_requested_at, t.retry_available_at,
		t.created_at, t.updated_at, la.attempt, la.state,
		COALESCE(la.error_class, ''), COALESCE(la.error_code, ''),
		(t.lease_until IS NOT NULL AND t.lease_until <= now())
		FROM tasks t
		JOIN LATERAL (
			SELECT attempt, state, error_class, error_code
			FROM task_attempts a
			WHERE a.task_id = t.id
			ORDER BY a.attempt DESC
			LIMIT 1
		) la ON true
		WHERE t.state = 'running'
		  AND ((la.state = 'failed' AND la.error_class = 'indeterminate')
		       OR (la.state IN ('claimed', 'authorized') AND t.lease_until <= now()))
		ORDER BY t.updated_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RecoveryItem
	for rows.Next() {
		var it RecoveryItem
		var t Task
		if err := rows.Scan(&t.ID, &t.ClientID, &t.WorkloadID, &t.Capability,
			&t.IdempotencyKey, &t.RequestHash, &t.State, &t.Attempts,
			&t.LeaseUntil, &t.StartedAt, &t.FinishedAt, &t.CancelRequestedAt,
			&t.RetryAvailableAt, &t.CreatedAt, &t.UpdatedAt,
			&it.AttemptNo, &it.Phase, &it.ErrorClass, &it.ErrorCode,
			&it.LeaseExpired); err != nil {
			return nil, err
		}
		it.SafeToRequeue = it.Phase == AttemptPhaseClaimed
		if it.Phase == AttemptPhaseClaimed {
			it.ErrorClass = string(domain.ErrorTransientUnsent)
			it.ErrorCode = "lease_expired_safe_unsent"
		} else if it.Phase == AttemptPhaseAuthorized {
			it.ErrorClass = string(domain.ErrorIndeterminate)
			it.ErrorCode = "authorized_expired"
		}
		it.Task = t
		items = append(items, it)
	}
	return items, rows.Err()
}

// RequeueForRetry moves a running task back to queued with a persisted
// availability time (backoff), records the attempt failure, and clears all
// running-only timestamps. The task becomes eligible for claiming again only
// at or after availableAt. Guarded by the expected state 'running'.
func (s *Store) RequeueForRetry(ctx context.Context, taskID uuid.UUID, attempt int, class, code string, availableAt time.Time) (Task, error) {
	var t Task
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE tasks
			SET state = 'queued',
			    retry_available_at = $2,
			    started_at = NULL,
			    finished_at = NULL,
			    lease_until = NULL,
			    cancel_requested_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND state = 'running' AND attempts = $3
			RETURNING `+taskColumns, taskID, availableAt, attempt)
		var err error
		t, err = scanTask(row)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE task_attempts
			SET state = 'failed', error_class = $3, error_code = $4, finished_at = now()
			WHERE task_id = $1 AND attempt = $2
			  AND state IN ('claimed', 'authorized', 'failed')`,
			taskID, attempt, class, code)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: attempt %d is not recoverable", ErrInvalidTransition, attempt)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, fmt.Errorf("%w: task %s is not running", ErrInvalidTransition, taskID)
		}
		return Task{}, err
	}
	return t, nil
}

// FinishTaskSucceeded persists the result and transitions running -> succeeded
// in one transaction, also closing the attempt as succeeded.
func (s *Store) FinishTaskSucceeded(ctx context.Context, taskID uuid.UUID, attempt int, res Result) (Task, error) {
	var t Task
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_results (task_id, content, finish_reason,
				usage_prompt_tokens, usage_completion_tokens, usage_total_tokens)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			taskID, res.Content, res.FinishReason,
			res.PromptTokens, res.CompletionTokens, res.TotalTokens); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			UPDATE tasks
			SET state = 'succeeded', finished_at = now(),
			    started_at = NULL, cancel_requested_at = NULL,
			    lease_until = NULL, updated_at = now()
			WHERE id = $1 AND state = 'running'
			RETURNING `+taskColumns, taskID)
		var err error
		t, err = scanTask(row)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE task_attempts
			SET state = 'succeeded', finished_at = now()
			WHERE task_id = $1 AND attempt = $2 AND state = 'authorized'`,
			taskID, attempt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: attempt %d is not authorized", ErrInvalidTransition, attempt)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, fmt.Errorf("%w: task %s is not running", ErrInvalidTransition, taskID)
		}
		return Task{}, err
	}
	return t, nil
}

// FinishTaskFailed transitions running -> failed and records the attempt
// failure with its sanitized classification.
func (s *Store) FinishTaskFailed(ctx context.Context, taskID uuid.UUID, attempt int, class, code string) (Task, error) {
	var t Task
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE tasks
			SET state = 'failed', finished_at = now(),
			    started_at = NULL, cancel_requested_at = NULL,
			    lease_until = NULL, updated_at = now()
			WHERE id = $1 AND state = 'running'
			RETURNING `+taskColumns, taskID)
		var err error
		t, err = scanTask(row)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE task_attempts
			SET state = 'failed', error_class = $3, error_code = $4, finished_at = now()
			WHERE task_id = $1 AND attempt = $2`,
			taskID, attempt, class, code); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, fmt.Errorf("%w: task %s is not running", ErrInvalidTransition, taskID)
		}
		return Task{}, err
	}
	return t, nil
}

// FinishTaskCancelled transitions running -> cancelled (output discarded),
// recording the attempt as failed with the cancel_requested code. Used both
// for cancellation observed before the adapter call and for the output of a
// completed call that was cancelled meanwhile.
func (s *Store) FinishTaskCancelled(ctx context.Context, taskID uuid.UUID, attempt int) (Task, error) {
	var t Task
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE tasks
			SET state = 'cancelled', finished_at = now(),
			    started_at = NULL, cancel_requested_at = NULL,
			    lease_until = NULL, updated_at = now()
			WHERE id = $1 AND state = 'running'
			RETURNING `+taskColumns, taskID)
		var err error
		t, err = scanTask(row)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE task_attempts
			SET state = 'failed', error_class = 'internal', error_code = 'cancel_requested', finished_at = now()
			WHERE task_id = $1 AND attempt = $2`,
			taskID, attempt); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, fmt.Errorf("%w: task %s is not running", ErrInvalidTransition, taskID)
		}
		return Task{}, err
	}
	return t, nil
}
