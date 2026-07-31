package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

const (
	// maxWaitSeconds is the ceiling for Prefer: wait=N (product contract).
	maxWaitSeconds = 30
	// pollInterval is the polling cadence of the bounded synchronous wait.
	pollInterval = 250 * time.Millisecond
)

var (
	preferRe = regexp.MustCompile(`^wait=([0-9]+)$`)
	keyRe    = regexp.MustCompile(`^[ -~]{8,200}$`)
)

// --- response shapes (mirror openapi.yaml) --------------------------------

type taskResponse struct {
	ID         string    `json:"id"`
	Workload   string    `json:"workload"`
	Capability string    `json:"capability"`
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toTaskResponse(t postgres.Task, workload string) taskResponse {
	return taskResponse{
		ID:         t.ID.String(),
		Workload:   workload,
		Capability: t.Capability,
		Status:     string(t.State),
		Attempts:   t.Attempts,
		CreatedAt:  t.CreatedAt,
		UpdatedAt:  t.UpdatedAt,
	}
}

// workloadName resolves the policy name for a task, falling back to the
// numeric ID on lookup failure so reads never fail for a display field.
func (s *Server) workloadName(ctx context.Context, workloadID int64) string {
	p, err := s.store.GetWorkloadPolicyByID(ctx, workloadID)
	if err != nil {
		return strconv.FormatInt(workloadID, 10)
	}
	return p.Name
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type inferenceResult struct {
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason"`
	Usage        usage  `json:"usage"`
}

type classifiedError struct {
	Class   string `json:"class"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type taskResultResponse struct {
	Status string           `json:"status"`
	Result *inferenceResult `json:"result,omitempty"`
	Error  *classifiedError `json:"error,omitempty"`
}

// --- POST /v1/tasks -------------------------------------------------------

// handleCreateTask validates, authenticates, and durably persists a task,
// then optionally waits a bounded window for a terminal state.
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	ident, _ := clientFrom(r.Context())

	key := r.Header.Get("Idempotency-Key")
	if !keyRe.MatchString(key) {
		writeProblem(w, http.StatusBadRequest, codeMalformedRequest, "invalid request",
			"Idempotency-Key must be 8-200 printable ASCII characters")
		return
	}

	wait, err := parsePrefer(r.Header.Get("Prefer"))
	if err != nil {
		if errors.Is(err, errPreferTooLong) {
			writeProblem(w, http.StatusUnprocessableEntity, codeUnprocessable, "invalid request",
				fmt.Sprintf("Prefer wait must be 0-%d seconds", maxWaitSeconds))
			return
		}
		writeProblem(w, http.StatusBadRequest, codeMalformedRequest, "invalid request",
			`Prefer must have the form "wait=N"`)
		return
	}

	bodyLimit := s.cfg.API.BodyLimitBytes
	if bodyLimit <= 0 {
		bodyLimit = domain.MaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	sub, err := domain.DecodeTaskSubmission(r.Body)
	if err != nil {
		s.writeSubmissionError(w, r, err)
		return
	}

	policy, err := s.store.GetWorkloadPolicyByName(r.Context(), sub.Workload)
	if errors.Is(err, postgres.ErrNotFound) {
		writeProblem(w, http.StatusForbidden, codeForbidden, "forbidden",
			"workload unknown or not granted")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	has, err := s.store.ClientHasWorkload(r.Context(), ident.ClientID, policy.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !has {
		writeProblem(w, http.StatusForbidden, codeForbidden, "forbidden",
			"workload unknown or not granted")
		return
	}

	if _, ok := s.cfg.Capabilities[sub.Capability]; !ok {
		writeProblem(w, http.StatusUnprocessableEntity, codeUnprocessable, "invalid task",
			"unknown capability")
		return
	}

	if err := s.enforceGenerationPolicy(policy, sub); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, codeUnprocessable, "invalid task", err.Error())
		return
	}

	active, err := s.store.CountActiveTasks(r.Context(), ident.ClientID, policy.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if active >= int64(policy.MaxActiveTasks) {
		writeProblem(w, http.StatusTooManyRequests, codeAdmissionLimited, "admission limited",
			"client activity limit reached for this workload")
		return
	}

	hash, err := sub.RequestHash()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hashBytes, err := hex.DecodeString(hash)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	canon, err := sub.CanonicalBytes()
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	res, err := s.store.CreateTask(r.Context(), postgres.CreateTaskParams{
		ClientID:       ident.ClientID,
		WorkloadID:     policy.ID,
		Capability:     sub.Capability,
		IdempotencyKey: key,
		RequestHash:    hashBytes,
		Payload:        canon,
	})
	if errors.Is(err, postgres.ErrConflict) {
		writeProblem(w, http.StatusConflict, codeIdempotencyConflict, "idempotency conflict",
			"Idempotency-Key was already used with a different request")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	task, err := s.store.GetTaskByClient(r.Context(), res.TaskID, ident.ClientID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Location", "/v1/tasks/"+task.ID.String())

	if wait > 0 {
		done, resp, err := s.waitTerminal(r, task, ident, wait)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if done {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, toTaskResponse(task, policy.Name))
		return
	}

	writeJSON(w, http.StatusCreated, toTaskResponse(task, policy.Name))
}

// errPreferTooLong distinguishes a syntactically valid but out-of-range wait
// (422) from a malformed Prefer header (400).
var errPreferTooLong = errors.New("prefer wait too long")

func parsePrefer(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	m := preferRe.FindStringSubmatch(value)
	if m == nil {
		return 0, errors.New("malformed prefer header")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	if n > maxWaitSeconds {
		return 0, errPreferTooLong
	}
	return n, nil
}

// writeSubmissionError maps decode/validation failures to 400 (malformed
// bytes) or 422 (contract violations).
func (s *Server) writeSubmissionError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &maxErr):
		writeProblem(w, http.StatusBadRequest, codeMalformedRequest, "invalid request",
			"request body too large")
	case errors.Is(err, domain.ErrTrailingContent):
		writeProblem(w, http.StatusBadRequest, codeMalformedRequest, "invalid request",
			"trailing content after the JSON body")
	case errors.As(err, &syn), errors.As(err, &typ),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		writeProblem(w, http.StatusBadRequest, codeMalformedRequest, "invalid request",
			"invalid JSON body")
	default:
		// Validation errors name fields and bounds, never message content.
		writeProblem(w, http.StatusUnprocessableEntity, codeUnprocessable, "invalid task",
			err.Error())
	}
}

// enforceGenerationPolicy applies the workload policy's generation bounds to
// the normalized submission.
func (s *Server) enforceGenerationPolicy(policy postgres.WorkloadPolicy, sub *domain.TaskSubmission) error {
	var allowed map[string]config.GenerationConstraint
	if len(policy.AllowedGeneration) > 0 && string(policy.AllowedGeneration) != "{}" {
		if err := json.Unmarshal(policy.AllowedGeneration, &allowed); err != nil {
			return fmt.Errorf("workload policy data invalid: %w", err)
		}
	}
	if policy.MaxOutputTokens > 0 && sub.Generation.MaxTokens != nil &&
		*sub.Generation.MaxTokens > int64(policy.MaxOutputTokens) {
		return fmt.Errorf("max_tokens %d exceeds the workload limit %d",
			*sub.Generation.MaxTokens, policy.MaxOutputTokens)
	}
	for field, c := range allowed {
		switch field {
		case "max_tokens":
			if v := sub.Generation.MaxTokens; v != nil {
				if c.Min != nil && float64(*v) < *c.Min {
					return fmt.Errorf("max_tokens %d is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && float64(*v) > *c.Max {
					return fmt.Errorf("max_tokens %d exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "temperature":
			if v := sub.Generation.Temperature; v != nil {
				if c.Min != nil && *v < *c.Min {
					return fmt.Errorf("temperature %v is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && *v > *c.Max {
					return fmt.Errorf("temperature %v exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "top_p":
			if v := sub.Generation.TopP; v != nil {
				if c.Min != nil && *v < *c.Min {
					return fmt.Errorf("top_p %v is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && *v > *c.Max {
					return fmt.Errorf("top_p %v exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "min_p":
			if v := sub.Generation.MinP; v != nil {
				if c.Min != nil && *v < *c.Min {
					return fmt.Errorf("min_p %v is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && *v > *c.Max {
					return fmt.Errorf("min_p %v exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "top_k":
			if v := sub.Generation.TopK; v != nil {
				if c.Min != nil && float64(*v) < *c.Min {
					return fmt.Errorf("top_k %d is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && float64(*v) > *c.Max {
					return fmt.Errorf("top_k %d exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "seed":
			if v := sub.Generation.Seed; v != nil {
				if c.Min != nil && float64(*v) < *c.Min {
					return fmt.Errorf("seed %d is below the workload minimum %v", *v, *c.Min)
				}
				if c.Max != nil && float64(*v) > *c.Max {
					return fmt.Errorf("seed %d exceeds the workload maximum %v", *v, *c.Max)
				}
			}
		case "stop":
			if c.MaxItems != nil && len(sub.Generation.Stop) > *c.MaxItems {
				return fmt.Errorf("stop has %d entries, workload maximum %d",
					len(sub.Generation.Stop), *c.MaxItems)
			}
		}
	}
	return nil
}

// waitTerminal polls the durable task state until it reaches a terminal
// state, the wait budget elapses, or the client disconnects. It never
// mutates the task: a disconnect only stops polling and the task stays
// queued. Returns done=true when the task is terminal.
func (s *Server) waitTerminal(r *http.Request, task postgres.Task, ident auth.Identity, wait int) (bool, taskResultResponse, error) {
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if task.State.Terminal() {
			resp, err := s.buildResult(r.Context(), task)
			return true, resp, err
		}
		if time.Now().After(deadline) {
			return false, taskResultResponse{}, nil
		}
		select {
		case <-r.Context().Done():
			return false, taskResultResponse{}, nil
		case <-ticker.C:
		}
		var err error
		task, err = s.store.GetTaskByClient(r.Context(), task.ID, ident.ClientID)
		if err != nil {
			if r.Context().Err() != nil {
				return false, taskResultResponse{}, nil
			}
			return false, taskResultResponse{}, err
		}
	}
}

// buildResult maps a task (and its stored output) to the result envelope.
// The failed case is synthesized from the last attempt's sanitized
// classification; reasoning content is never persisted or returned.
func (s *Server) buildResult(ctx context.Context, t postgres.Task) (taskResultResponse, error) {
	switch t.State {
	case domain.TaskQueued, domain.TaskRunning:
		return taskResultResponse{Status: "pending"}, nil
	case domain.TaskSucceeded:
		res, err := s.store.GetResultByTaskID(ctx, t.ID)
		if errors.Is(err, postgres.ErrNotFound) {
			// Result row purged by retention: terminal but no payload.
			return taskResultResponse{Status: "expired"}, nil
		}
		if err != nil {
			return taskResultResponse{}, err
		}
		return taskResultResponse{
			Status: "succeeded",
			Result: &inferenceResult{
				Content:      res.Content,
				FinishReason: res.FinishReason,
				Usage: usage{
					PromptTokens:     res.PromptTokens,
					CompletionTokens: res.CompletionTokens,
					TotalTokens:      res.TotalTokens,
				},
			},
		}, nil
	case domain.TaskFailed:
		att, err := s.store.GetLastAttempt(ctx, t.ID)
		class, code := "internal", "unknown"
		if err == nil {
			if att.ErrorClass != nil {
				class = *att.ErrorClass
			}
			if att.ErrorCode != nil {
				code = *att.ErrorCode
			}
		} else if !errors.Is(err, postgres.ErrNotFound) {
			return taskResultResponse{}, err
		}
		return taskResultResponse{
			Status: "failed",
			Error:  &classifiedError{Class: class, Code: code, Message: messageForCode(code)},
		}, nil
	case domain.TaskCancelled:
		return taskResultResponse{Status: "cancelled"}, nil
	}
	return taskResultResponse{Status: "pending"}, nil
}

// messageForCode maps stable attempt codes to sanitized operator-facing
// messages. Provider output never reaches this path.
func messageForCode(code string) string {
	switch code {
	case "backend_rejected":
		return "the inference backend rejected the request"
	case "output_invalid":
		return "the inference output failed validation"
	case "workload_timeout":
		return "the workload time limit expired"
	case "cancel_requested":
		return "the task was cancelled after the current inference"
	case "indeterminate":
		return "the outcome is unknown; contact the operator before retrying"
	default:
		return "the task failed; see the error code for details"
	}
}

// --- GET /v1/tasks/{task_id} ----------------------------------------------

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	ident, _ := clientFrom(r.Context())
	id, err := uuid.Parse(r.PathValue("task_id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	task, err := s.store.GetTaskByClient(r.Context(), id, ident.ClientID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(task, s.workloadName(r.Context(), task.WorkloadID)))
}

// --- GET /v1/tasks/{task_id}/result ---------------------------------------

func (s *Server) handleGetResult(w http.ResponseWriter, r *http.Request) {
	ident, _ := clientFrom(r.Context())
	id, err := uuid.Parse(r.PathValue("task_id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	task, err := s.store.GetTaskByClient(r.Context(), id, ident.ClientID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	resp, err := s.buildResult(r.Context(), task)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- POST /v1/tasks/{task_id}/cancel --------------------------------------

// handleCancelTask cancels a queued task immediately, records durable cancel
// intent for a running task, and answers idempotently for already-cancelled
// tasks. Succeeded and failed tasks yield a conflict.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	ident, _ := clientFrom(r.Context())
	id, err := uuid.Parse(r.PathValue("task_id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	ctx := r.Context()

	cancelled, err := s.store.CancelQueuedTask(ctx, id, ident.ClientID)
	if err == nil {
		writeJSON(w, http.StatusOK, toTaskResponse(cancelled, s.workloadName(ctx, cancelled.WorkloadID)))
		return
	}
	if !errors.Is(err, postgres.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}

	task, err := s.store.GetTaskByClient(ctx, id, ident.ClientID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, codeNotFound, "not found", "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	switch task.State {
	case domain.TaskRunning:
		updated, err := s.store.RequestCancelRunning(ctx, id, ident.ClientID)
		if errors.Is(err, postgres.ErrNotFound) {
			writeProblem(w, http.StatusConflict, codeConflict, "conflict",
				"task state changed while cancelling")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toTaskResponse(updated, s.workloadName(ctx, updated.WorkloadID)))
	case domain.TaskCancelled:
		// Idempotent: cancelling an already-cancelled task is a no-op.
		writeJSON(w, http.StatusOK, toTaskResponse(task, s.workloadName(ctx, task.WorkloadID)))
	default: // succeeded, failed
		writeProblem(w, http.StatusConflict, codeConflict, "conflict",
			fmt.Sprintf("task is %s and cannot be cancelled", task.State))
	}
}

// internalError logs the cause (server-side only) and answers with a
// sanitized 500.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed",
		"request_id", requestIDFrom(r.Context()),
		"path", r.URL.Path,
		"error", err)
	writeProblem(w, http.StatusInternalServerError, codeInternal,
		"internal error", "the request could not be completed")
}
