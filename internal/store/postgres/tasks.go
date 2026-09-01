package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// Task is the operational row of a submitted task. The normalized request body
// lives in task_payloads and the output in task_results; both are deliberately
// separate tables for retention and access control.
type Task struct {
	ID                uuid.UUID
	ClientID          int64
	WorkloadID        int64
	Capability        string
	IdempotencyKey    string
	RequestHash       []byte
	State             domain.TaskState
	Attempts          int
	LeaseUntil        *time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CancelRequestedAt *time.Time
	RetryAvailableAt  *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TaskWithPayload is a claimed or read task together with its normalized
// request payload (canonical JSON bytes).
type TaskWithPayload struct {
	Task
	Payload []byte
	// WorkloadSlug is the immutable policy identity carried by a claim.
	WorkloadSlug string
}

// CreateTaskParams is the input for an atomic idempotent task creation.
type CreateTaskParams struct {
	ClientID       int64
	WorkloadID     int64
	Capability     string
	IdempotencyKey string
	RequestHash    []byte
	// Payload is the canonical JSON of the normalized submission.
	Payload []byte
}

// CreateTaskResult distinguishes a fresh creation from an idempotent replay.
type CreateTaskResult struct {
	TaskID  uuid.UUID
	Created bool
}

const taskColumns = `id, client_id, workload_id, capability, idempotency_key,
	request_hash, state, attempts, lease_until, started_at, finished_at,
	cancel_requested_at, retry_available_at, created_at, updated_at`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ClientID, &t.WorkloadID, &t.Capability,
		&t.IdempotencyKey, &t.RequestHash, &t.State, &t.Attempts,
		&t.LeaseUntil, &t.StartedAt, &t.FinishedAt, &t.CancelRequestedAt,
		&t.RetryAvailableAt, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// createTaskFixture inserts a task row for package-local database fixtures.
// Production submission must use SubmitTask, which performs authentication,
// durable authorization, policy validation, admission and locking atomically.
// Keeping this helper unexported prevents application code from bypassing H00.
func (s *Store) createTaskFixture(ctx context.Context, p CreateTaskParams) (CreateTaskResult, error) {
	var res CreateTaskResult
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO tasks (client_id, workload_id, capability, idempotency_key, request_hash)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (client_id, idempotency_key) DO NOTHING
			RETURNING `+taskColumns,
			p.ClientID, p.WorkloadID, p.Capability, p.IdempotencyKey, p.RequestHash)
		t, err := scanTask(row)
		if err == nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO task_payloads (task_id, payload) VALUES ($1, $2)`,
				t.ID, string(p.Payload)); err != nil {
				return fmt.Errorf("insert task payload: %w", err)
			}
			res = CreateTaskResult{TaskID: t.ID, Created: true}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert task: %w", err)
		}
		// Conflict path: the key exists. Distinguish replay from mismatch.
		var existing Task
		existing, err = s.getTaskByKey(ctx, tx, p.ClientID, p.IdempotencyKey)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing.RequestHash, p.RequestHash) {
			return fmt.Errorf("%w: idempotency key %q reused with a different request",
				ErrConflict, p.IdempotencyKey)
		}
		res = CreateTaskResult{TaskID: existing.ID, Created: false}
		return nil
	})
	return res, err
}

func (s *Store) getTaskByKey(ctx context.Context, q pgxQuerier, clientID int64, key string) (Task, error) {
	row := q.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM tasks
		WHERE client_id = $1 AND idempotency_key = $2`,
		clientID, key)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// GetTask returns a task by ID.
func (s *Store) GetTask(ctx context.Context, taskID uuid.UUID) (Task, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, taskID)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// GetTaskByClient returns a task scoped to a client. Cross-client access is
// indistinguishable from a missing task (ErrNotFound).
func (s *Store) GetTaskByClient(ctx context.Context, taskID uuid.UUID, clientID int64) (Task, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+taskColumns+` FROM tasks
		WHERE id = $1 AND client_id = $2`, taskID, clientID)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// CancelQueuedTask atomically cancels a queued task owned by the client.
// Returns ErrNotFound when the task does not exist for the client or is no
// longer queued.
func (s *Store) CancelQueuedTask(ctx context.Context, taskID uuid.UUID, clientID int64) (Task, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE tasks
		SET state = 'cancelled', finished_at = now(), retry_available_at = NULL, updated_at = now()
		WHERE id = $1 AND client_id = $2 AND state = 'queued'
		RETURNING `+taskColumns, taskID, clientID)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// RequestCancelRunning durably records cancel intent for a running task
// owned by the client. The worker honors it after the current inference
// completes and discards the output. Returns ErrNotFound when the task does
// not exist for the client or is not running.
func (s *Store) RequestCancelRunning(ctx context.Context, taskID uuid.UUID, clientID int64) (Task, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE tasks
		SET cancel_requested_at = now(), updated_at = now()
		WHERE id = $1 AND client_id = $2 AND state = 'running'
		RETURNING `+taskColumns, taskID, clientID)
	t, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// GetTaskWithPayload returns a task and its payload, or ErrNotFound.
func (s *Store) GetTaskWithPayload(ctx context.Context, taskID uuid.UUID) (TaskWithPayload, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT t.id, t.client_id, t.workload_id, t.capability, t.idempotency_key,
		       t.request_hash, t.state, t.attempts, t.lease_until, t.started_at,
		       t.finished_at, t.cancel_requested_at, t.retry_available_at,
		       t.created_at, t.updated_at, p.payload
		FROM tasks t
		JOIN task_payloads p ON p.task_id = t.id
		WHERE t.id = $1`, taskID)
	var tw TaskWithPayload
	err := row.Scan(&tw.ID, &tw.ClientID, &tw.WorkloadID, &tw.Capability,
		&tw.IdempotencyKey, &tw.RequestHash, &tw.State, &tw.Attempts,
		&tw.LeaseUntil, &tw.StartedAt, &tw.FinishedAt, &tw.CancelRequestedAt,
		&tw.RetryAvailableAt, &tw.CreatedAt, &tw.UpdatedAt, &tw.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskWithPayload{}, ErrNotFound
	}
	return tw, err
}

// ListTasksByClient returns the most recent tasks of a client for polling.
func (s *Store) ListTasksByClient(ctx context.Context, clientID int64, limit int) ([]Task, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+taskColumns+` FROM tasks
		WHERE client_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, clientID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// TransitionTask moves a task from an expected state to a target state in one
// atomic UPDATE guarded by the expected-state predicate. Timestamps are set by
// the database: started_at only when entering running, finished_at only when
// entering a terminal state, lease_until only while running.
func (s *Store) TransitionTask(ctx context.Context, taskID uuid.UUID, from, to domain.TaskState) error {
	if !from.CanTransitionTo(to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	leaseSeconds := int64(0)
	if to == domain.TaskRunning {
		leaseSeconds = int64(defaultLease.Seconds())
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tasks
		SET state = $3,
		    updated_at = now(),
		    started_at = CASE WHEN $3 = 'running' THEN now() ELSE NULL END,
		    finished_at = CASE WHEN $3 IN ('succeeded', 'failed', 'cancelled') THEN now() ELSE NULL END,
		    lease_until = CASE WHEN $3 = 'running' THEN now() + ($4 * interval '1 second') ELSE NULL END,
		    retry_available_at = CASE WHEN $3 = 'queued' THEN retry_available_at ELSE NULL END
		WHERE id = $1 AND state = $2`,
		taskID, string(from), string(to), leaseSeconds)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	cur, err := s.GetTask(ctx, taskID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: task %s is %s, not %s", ErrInvalidTransition, taskID, cur.State, from)
}

// defaultLease is the v1 worker lease duration applied by TransitionTask.
// The worker renews it via RenewTaskLease and P07 defines recovery when a
// lease expires.
const defaultLease = 60 * time.Second

// RenewTaskLease extends the lease of a running task.
func (s *Store) RenewTaskLease(ctx context.Context, taskID uuid.UUID, duration time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("lease duration must be positive")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tasks
		SET lease_until = now() + ($2 * interval '1 second'), updated_at = now()
		WHERE id = $1 AND state = 'running'`,
		taskID, int64(duration.Seconds()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	cur, err := s.GetTask(ctx, taskID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: task %s is %s, not running", ErrInvalidTransition, taskID, cur.State)
}

// ClaimedTask is a successfully claimed task together with the scheduler's
// effective priority that selected it (0 on the FIFO claim path).
type ClaimedTask struct {
	TaskWithPayload
	EffectivePriority int
}

// ClaimNextQueued atomically claims the oldest queued task (FIFO), records
// the attempt with its claiming worker, and returns the task with its
// payload. Row locking (FOR UPDATE SKIP LOCKED) guarantees each task is
// claimed exactly once even across concurrent workers. This policy-free
// primitive is the base order; the scheduler policy ordering lives in
// ClaimNextByPolicy.
func (s *Store) ClaimNextQueued(ctx context.Context, workerID uuid.UUID, lease time.Duration) (TaskWithPayload, error) {
	ct, err := s.claimSelect(ctx, workerID, lease, `
		SELECT t.id, t.client_id, t.workload_id, w.name, t.capability, t.idempotency_key,
		       t.request_hash, t.state, t.attempts, t.lease_until, t.started_at,
		       t.finished_at, t.cancel_requested_at, t.retry_available_at,
		       t.created_at, t.updated_at,
		       p.payload, 0::int AS effective_priority
		FROM tasks t
		JOIN task_payloads p ON p.task_id = t.id
		JOIN workload_policies w ON w.id = t.workload_id
		WHERE t.state = 'queued'
		ORDER BY t.created_at, t.id
		FOR UPDATE OF t SKIP LOCKED
		LIMIT 1`)
	if err != nil {
		return TaskWithPayload{}, err
	}
	return ct.TaskWithPayload, nil
}

// ClaimNextByPolicy claims the single best eligible task under the
// broker-owned policy ordering. The query is built by the scheduler package
// (the policy lives there); it must select the same claim projection columns as
// claimSelect plus an effective_priority integer, ordered, and terminated
// with FOR UPDATE SKIP LOCKED LIMIT 1. Eligibility (state and attempts) is
// part of the query so SQL and pure policy cannot disagree; authorization is a
// worker preflight concern.
func (s *Store) ClaimNextByPolicy(ctx context.Context, workerID uuid.UUID, lease time.Duration, query string, args ...any) (ClaimedTask, error) {
	return s.claimSelect(ctx, workerID, lease, query, args...)
}

// claimSelect is the single claim transaction: select exactly one eligible
// row (FOR UPDATE SKIP LOCKED), flip it to running with a fresh attempt
// number and lease, and record the attempt row. Every claim in the product
// flows through here.
func (s *Store) claimSelect(ctx context.Context, workerID uuid.UUID, lease time.Duration, query string, args ...any) (ClaimedTask, error) {
	if lease <= 0 {
		lease = defaultLease
	}
	var claimed ClaimedTask
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var t Task
		var payload []byte
		var effective int
		var workloadSlug string
		err := tx.QueryRow(ctx, query, args...).Scan(
			&t.ID, &t.ClientID, &t.WorkloadID, &workloadSlug, &t.Capability, &t.IdempotencyKey,
			&t.RequestHash, &t.State, &t.Attempts, &t.LeaseUntil, &t.StartedAt,
			&t.FinishedAt, &t.CancelRequestedAt, &t.RetryAvailableAt,
			&t.CreatedAt, &t.UpdatedAt,
			&payload, &effective)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoTaskAvailable
		}
		if err != nil {
			return err
		}
		var newAttempts int
		err = tx.QueryRow(ctx, `
			UPDATE tasks
			SET state = 'running',
			    attempts = attempts + 1,
			    started_at = now(),
			    lease_until = now() + ($2 * interval '1 second'),
			    retry_available_at = NULL,
			    updated_at = now()
			WHERE id = $1 AND state = 'queued'
			RETURNING attempts`,
			t.ID, int64(lease.Seconds())).Scan(&newAttempts)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO task_attempts (task_id, attempt, worker_id, state)
			VALUES ($1, $2, $3, 'claimed')`,
			t.ID, newAttempts, workerID); err != nil {
			return err
		}
		t.State = domain.TaskRunning
		t.Attempts = newAttempts
		now := time.Now().UTC()
		t.StartedAt = &now
		leaseUntil := now.Add(lease)
		t.LeaseUntil = &leaseUntil
		claimed = ClaimedTask{
			TaskWithPayload:   TaskWithPayload{Task: t, Payload: payload, WorkloadSlug: workloadSlug},
			EffectivePriority: effective,
		}
		return nil
	})
	return claimed, err
}

// pgxQuerier is the subset of pgx.Tx and pgxpool.Pool used by helpers.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// withTx runs fn inside a transaction, rolling back on error and committing on
// success.
func (s *Store) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
