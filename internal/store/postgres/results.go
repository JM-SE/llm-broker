package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Result is the stored inference output of a succeeded task. It never
// carries reasoning content; retention is bounded by the result window.
type Result struct {
	TaskID           uuid.UUID
	Content          string
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CreatedAt        time.Time
}

// SaveResult persists the output of a succeeded task. It is called by the
// worker after validation; content must already be sanitized (reasoning is
// never persisted).
func (s *Store) SaveResult(ctx context.Context, taskID uuid.UUID, content, finishReason string,
	promptTokens, completionTokens, totalTokens int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_results (task_id, content, finish_reason, usage_prompt_tokens,
		                          usage_completion_tokens, usage_total_tokens)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		taskID, content, finishReason, promptTokens, completionTokens, totalTokens)
	return err
}

// MarkAttemptFailed records the sanitized outcome of a failed attempt: the
// error class and stable code, never raw provider bodies. Worker execution
// calls it only after the authorized boundary.
func (s *Store) MarkAttemptFailed(ctx context.Context, taskID uuid.UUID, attempt int, class, code string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE task_attempts
		SET state = 'failed', error_class = $3, error_code = $4, finished_at = now()
		WHERE task_id = $1 AND attempt = $2`,
		taskID, attempt, class, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetResultByTaskID returns the result row of a task.
func (s *Store) GetResultByTaskID(ctx context.Context, taskID uuid.UUID) (Result, error) {
	var r Result
	err := s.pool.QueryRow(ctx, `
		SELECT task_id, content, finish_reason, usage_prompt_tokens, usage_completion_tokens,
		       usage_total_tokens, created_at
		FROM task_results WHERE task_id = $1`, taskID).Scan(
		&r.TaskID, &r.Content, &r.FinishReason, &r.PromptTokens,
		&r.CompletionTokens, &r.TotalTokens, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	return r, err
}

// Attempt is one recorded execution attempt of a task. It stores only the
// sanitized classification and stable code, never raw provider bodies.
type Attempt struct {
	TaskID     uuid.UUID
	Attempt    int
	WorkerID   uuid.UUID
	State      string
	ErrorClass *string
	ErrorCode  *string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// GetLastAttempt returns the highest-numbered attempt of a task.
func (s *Store) GetLastAttempt(ctx context.Context, taskID uuid.UUID) (Attempt, error) {
	var a Attempt
	err := s.pool.QueryRow(ctx, `
		SELECT task_id, attempt, worker_id, state, error_class, error_code,
		       started_at, finished_at
		FROM task_attempts
		WHERE task_id = $1
		ORDER BY attempt DESC
		LIMIT 1`, taskID).Scan(
		&a.TaskID, &a.Attempt, &a.WorkerID, &a.State, &a.ErrorClass,
		&a.ErrorCode, &a.StartedAt, &a.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attempt{}, ErrNotFound
	}
	return a, err
}

// CountActiveTasks counts non-terminal tasks (queued or running) of one
// client for one workload, used by admission control.
func (s *Store) CountActiveTasks(ctx context.Context, clientID, workloadID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM tasks
		WHERE client_id = $1 AND workload_id = $2
		  AND state IN ('queued', 'running')`,
		clientID, workloadID).Scan(&n)
	return n, err
}
