package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/metrics"
	"github.com/JM-SE/llm-broker/internal/store/postgres"

	"github.com/google/uuid"
)

// execute runs one claimed task end to end: policy lookup, adapter
// resolution, cancel checks, the adapter call with a workload deadline and
// lease renewal, output validation, and the terminal transaction. It never
// runs two calls concurrently: the loop is strictly serial.
func (w *Worker) execute(ctx context.Context, tw postgres.TaskWithPayload) error {
	log := w.log.With("task_id", tw.ID)
	attempt := tw.Attempts
	// All persistence runs on a context that survives worker shutdown: once
	// a task is claimed, its terminal outcome is recorded even if the
	// process receives SIGTERM mid-call.
	execCtx := context.WithoutCancel(ctx)

	policy, err := w.store.GetWorkloadPolicyByID(execCtx, tw.WorkloadID)
	if err != nil {
		return fmt.Errorf("load workload policy: %w", err)
	}

	adapter, err := w.adapterFor(tw.Capability, policy)
	if err != nil {
		// Configuration error: fail the worker, not the task. The process
		// exits and the operator fixes the configuration.
		return fmt.Errorf("resolve adapter: %w", err)
	}

	sub, err := decodePayload(tw.Payload)
	if err != nil {
		log.Error("task payload is corrupt; marking failed", "error", err)
		_, err = w.store.FinishTaskFailed(execCtx, tw.ID, attempt, string(domain.ErrorInternal), "invalid_payload")
		w.metrics.IncInference(string(domain.ErrorInternal))
		return err
	}

	// Cancellation observed before the adapter call: no inference happens.
	if tw.CancelRequestedAt != nil {
		log.Info("cancelling before inference (cancel requested while queued or claimed)")
		_, err := w.store.FinishTaskCancelled(execCtx, tw.ID, attempt)
		w.metrics.IncInference(metrics.ClassCancelled)
		return err
	}

	timeout := time.Duration(policy.WorkloadTimeoutSeconds) * time.Second
	// The call survives worker shutdown: it runs until its own workload
	// deadline, and cancellation of the worker process never preempts it.
	callCtx, cancel := context.WithTimeout(execCtx, timeout)
	defer cancel()

	done := make(chan struct{})
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.renewLease(callCtx, tw.ID, done)
	}()

	resp, err := adapter.Infer(callCtx, &backend.Request{
		Messages:   sub.Messages,
		Generation: sub.Generation,
		Response:   sub.Response,
	})
	close(done)

	if err != nil {
		var be *backend.Error
		if !errors.As(err, &be) {
			log.Error("adapter returned an unclassified error; marking failed", "error", err)
			_, err = w.store.FinishTaskFailed(execCtx, tw.ID, attempt, string(domain.ErrorInternal), "adapter_internal")
			w.metrics.IncInference(string(domain.ErrorInternal))
			return err
		}
		return w.handleBackendError(execCtx, log, tw, policy, attempt, be)
	}

	// Success path: a cancellation requested while the call was running
	// discards the output and cancels the task. The refresh runs on the
	// call context so a worker shutdown does not interrupt persistence.
	cur, err := w.store.GetTask(callCtx, tw.ID)
	if err != nil {
		return fmt.Errorf("refresh task state: %w", err)
	}
	if cur.CancelRequestedAt != nil {
		log.Info("cancelling after inference completed (cancel requested during call); output discarded")
		_, err := w.store.FinishTaskCancelled(execCtx, tw.ID, attempt)
		w.metrics.IncInference(metrics.ClassCancelled)
		return err
	}

	if err := validateOutput(policy, sub.Response, resp); err != nil {
		log.Error("output failed validation; marking failed", "error", err)
		_, err = w.store.FinishTaskFailed(execCtx, tw.ID, attempt, string(domain.ErrorOutputInvalid), "output_invalid")
		w.metrics.IncInference(string(domain.ErrorOutputInvalid))
		return err
	}

	_, err = w.store.FinishTaskSucceeded(execCtx, tw.ID, attempt, postgres.Result{
		Content:          resp.Content,
		FinishReason:     string(resp.FinishReason),
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	})
	if err != nil {
		return fmt.Errorf("persist success: %w", err)
	}
	w.metrics.IncInference(metrics.ClassSucceeded)
	log.Info("task succeeded", "attempts", attempt, "finish_reason", resp.FinishReason)
	return nil
}

// handleBackendError applies the retry/recovery policy to a classified
// adapter error.
func (w *Worker) handleBackendError(execCtx context.Context, log *slog.Logger, tw postgres.TaskWithPayload, policy postgres.WorkloadPolicy, attempt int, be *backend.Error) error {
	switch be.Class {
	case domain.ErrorTransientUnsent, domain.ErrorTransientSafe:
		// A cancellation requested while the call was running cancels the
		// task instead of retrying (the output is discarded either way).
		cur, err := w.store.GetTask(execCtx, tw.ID)
		if err != nil {
			return fmt.Errorf("refresh task state: %w", err)
		}
		if cur.CancelRequestedAt != nil {
			log.Info("cancelling after transient failure (cancel requested during call)")
			_, err := w.store.FinishTaskCancelled(execCtx, tw.ID, attempt)
			w.metrics.IncInference(metrics.ClassCancelled)
			return err
		}
		if attempt < policy.MaxAttempts {
			delay := retryDelay(policy.RetryDelaysSeconds, attempt, w.rng, w.now)
			log.Info("retrying task after transient failure",
				"attempt", attempt, "max_attempts", policy.MaxAttempts,
				"class", be.Class, "code", be.Code, "backoff", delay)
			_, err := w.store.RequeueForRetry(execCtx, tw.ID, attempt, string(be.Class), be.Code, w.now().Add(delay))
			w.metrics.IncInference(string(be.Class))
			return err
		}
		log.Warn("attempt budget exhausted; marking failed",
			"attempt", attempt, "max_attempts", policy.MaxAttempts, "class", be.Class, "code", be.Code)
		_, err = w.store.FinishTaskFailed(execCtx, tw.ID, attempt, string(be.Class), be.Code)
		w.metrics.IncInference(string(be.Class))
		return err
	case domain.ErrorIndeterminate:
		// Ambiguous outcome: the backend may have accepted the request.
		// Record the attempt, leave the task running with an expiring
		// lease, and block all new inference until the operator resolves
		// it. Never auto-retry.
		log.Error("indeterminate outcome; blocking new inference until operator resolution",
			"class", be.Class, "code", be.Code)
		w.metrics.IncInference(string(domain.ErrorIndeterminate))
		return w.store.MarkAttemptFailed(execCtx, tw.ID, attempt, string(be.Class), be.Code)
	default:
		// backend_rejected, output_invalid, client_invalid, policy_rejected,
		// internal: non-retryable, terminal failure.
		log.Error("terminal failure", "class", be.Class, "code", be.Code)
		_, err := w.store.FinishTaskFailed(execCtx, tw.ID, attempt, string(be.Class), be.Code)
		w.metrics.IncInference(string(be.Class))
		return err
	}
}

// retryDelay returns the bounded backoff for an attempt: the configured
// base delay (clamped to the last configured entry) plus up to 50% jitter,
// at least one second.
func retryDelay(delays []int, attempt int, rng *rand.Rand, now func() time.Time) time.Duration {
	base := 1
	if len(delays) > 0 {
		i := attempt - 1
		if i >= len(delays) {
			i = len(delays) - 1
		}
		if delays[i] > 0 {
			base = delays[i]
		}
	}
	jitter := 0
	if base > 1 && rng != nil {
		jitter = rng.Intn(base/2 + 1)
	}
	return time.Duration(base+jitter) * time.Second
}

// renewLease extends the task lease while the adapter call runs so the
// task is never considered stale during execution.
func (w *Worker) renewLease(ctx context.Context, taskID uuid.UUID, done <-chan struct{}) {
	interval := w.policy.Lease / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := w.store.RenewTaskLease(renewCtx, taskID, w.policy.Lease)
			cancel()
			if err != nil {
				w.log.Error("lease renewal failed", "task_id", taskID, "error", err)
			}
		}
	}
}

// adapterFor resolves the capability to its configured backend instance and
// verifies the workload's required features are declared.
func (w *Worker) adapterFor(capability string, policy postgres.WorkloadPolicy) (backend.Adapter, error) {
	capCfg, ok := w.capabilities[capability]
	if !ok {
		return nil, fmt.Errorf("capability %q is not configured", capability)
	}
	backendCfg, ok := w.backends[capCfg.Backend]
	if !ok {
		return nil, fmt.Errorf("backend %q of capability %q is not configured", capCfg.Backend, capability)
	}
	if err := backend.CheckFeatures(policy.RequiredFeatures, backendCfg.Features); err != nil {
		return nil, fmt.Errorf("workload requires undeclared features: %w", err)
	}
	return w.registry.New(backendCfg)
}

// decodePayload parses the canonical submission bytes persisted by the API.
func decodePayload(raw []byte) (*domain.TaskSubmission, error) {
	var sub domain.TaskSubmission
	if err := json.Unmarshal(raw, &sub); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if err := sub.Validate(); err != nil {
		return nil, fmt.Errorf("payload validation: %w", err)
	}
	return &sub, nil
}

// validateOutput enforces the size bound and the response contract before
// any output is persisted. Reasoning content never exists in this type.
func validateOutput(policy postgres.WorkloadPolicy, rc domain.ResponseContract, resp *backend.Response) error {
	maxContent := int64(policy.MaxOutputTokens) * 4 // ~4 bytes per token
	if int64(len(resp.Content)) > maxContent {
		return fmt.Errorf("output size %d exceeds the workload limit %d bytes", len(resp.Content), maxContent)
	}
	switch rc.Kind {
	case domain.ResponseText:
		return nil
	case domain.ResponseJSONObject:
		if !json.Valid([]byte(resp.Content)) {
			return fmt.Errorf("output is not valid JSON")
		}
		return nil
	case domain.ResponseJSONSchema:
		if rc.Schema == nil {
			return fmt.Errorf("response contract requires a schema")
		}
		return rc.Schema.Validate([]byte(resp.Content))
	}
	return fmt.Errorf("unsupported response kind %q", rc.Kind)
}
