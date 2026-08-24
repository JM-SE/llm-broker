package worker

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

var textResponse = domain.ResponseContract{Kind: domain.ResponseText}

type countingAdapter struct {
	calls atomic.Int32
}

func (a *countingAdapter) Type() string { return "counting" }

func (a *countingAdapter) Infer(context.Context, *backend.Request) (*backend.Response, error) {
	a.calls.Add(1)
	return &backend.Response{
		Content:      "unexpected inference",
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

type boundaryAdapter struct {
	calls      atomic.Int32
	beforeCall func()
}

func (a *boundaryAdapter) Type() string { return "boundary-spy" }

func (a *boundaryAdapter) Infer(context.Context, *backend.Request) (*backend.Response, error) {
	if a.beforeCall != nil {
		a.beforeCall()
	}
	a.calls.Add(1)
	return &backend.Response{
		Content:      "boundary response",
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func newTask(t *testing.T, maxAttempts int, retryDelays []int) (postgres.Client, postgres.WorkloadPolicy, uuid.UUID) {
	t.Helper()
	clearTasks(t) // no leftover tasks (e.g. unresolved indeterminate) may block this test
	client := mustClient(t)
	policy := mustPolicy(t, "wpolicy", maxAttempts, retryDelays)
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, "wpolicy", "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000000"), payload)
	return client, policy, taskID
}

func TestSuccessEndToEnd(t *testing.T) {
	_, _, taskID := newTask(t, 3, nil)
	w, _, _ := newWorkerEnv(t, "none", 0)
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskSucceeded, 10*time.Second)
	if task.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", task.Attempts)
	}
	res, err := rtStore.GetResultByTaskID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if !strings.HasPrefix(res.Content, "fake response") {
		t.Errorf("content %q missing fake prefix", res.Content)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	// A flaky adapter fails the first call with a safe transient error and
	// succeeds afterwards; the worker must retry exactly once.
	flaky := &flakyAdapter{remainingFailures: 1}
	client := mustClient(t)
	policy := mustPolicy(t, "wpolicy", 3, []int{1})
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, policy.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000000"), payload)

	w, leader, policy2 := newWorkerEnv(t, "none", 0)
	_ = policy2
	_ = leader
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) { return flaky, nil })
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskSucceeded, 15*time.Second)
	if task.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one transient retry)", task.Attempts)
	}
	last, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatalf("last attempt: %v", err)
	}
	if last.State != "succeeded" {
		t.Errorf("last attempt state = %s, want succeeded", last.State)
	}
}

// flakyAdapter fails its first N Infer calls with a safe transient error and
// succeeds afterwards (deterministic, no clock dependency).
type flakyAdapter struct {
	remainingFailures int
}

func (a *flakyAdapter) Type() string { return "flaky" }

func (a *flakyAdapter) Infer(ctx context.Context, req *backend.Request) (*backend.Response, error) {
	if a.remainingFailures > 0 {
		a.remainingFailures--
		return nil, backend.NewError(domain.ErrorTransientSafe, "backend_error", "simulated transient failure")
	}
	return &backend.Response{
		Content:      "flaky ok",
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

// indeterminateOnceAdapter returns an indeterminate outcome on its first
// call and succeeds afterwards (the same instance serves both tasks).
type indeterminateOnceAdapter struct {
	failed bool
}

func (a *indeterminateOnceAdapter) Type() string { return "indeterminate-once" }

func (a *indeterminateOnceAdapter) Infer(ctx context.Context, req *backend.Request) (*backend.Response, error) {
	if !a.failed {
		a.failed = true
		return nil, backend.NewError(domain.ErrorIndeterminate, "request_interrupted", "simulated ambiguous outcome")
	}
	return &backend.Response{
		Content:      "recovered ok",
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func TestMaxAttemptsExhaustedFails(t *testing.T) {
	_, _, taskID := newTask(t, 1, []int{1})
	w, _, _ := newWorkerEnv(t, "transient", 0)
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskFailed, 10*time.Second)
	if task.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", task.Attempts)
	}
	att, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if att.ErrorClass == nil || *att.ErrorClass != string(domain.ErrorTransientSafe) {
		t.Errorf("error class = %v, want transient_safe", att.ErrorClass)
	}
}

func TestBackendRejectedFailsImmediately(t *testing.T) {
	_, _, taskID := newTask(t, 3, nil)
	w, _, _ := newWorkerEnv(t, "reject", 0)
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskFailed, 10*time.Second)
	if task.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on backend_rejected)", task.Attempts)
	}
	att, _ := rtStore.GetLastAttempt(context.Background(), taskID)
	if att.ErrorClass == nil || *att.ErrorClass != string(domain.ErrorBackendRejected) {
		t.Errorf("error class = %v, want backend_rejected", att.ErrorClass)
	}
}

func TestMalformedOutputFails(t *testing.T) {
	_, _, taskID := newTask(t, 3, nil)
	w, _, _ := newWorkerEnv(t, "malformed", 0)
	startWorker(t, w)

	task := waitForState(t, taskID, domain.TaskFailed, 10*time.Second)
	if task.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on output_invalid)", task.Attempts)
	}
	att, _ := rtStore.GetLastAttempt(context.Background(), taskID)
	if att.ErrorClass == nil || *att.ErrorClass != string(domain.ErrorOutputInvalid) {
		t.Errorf("error class = %v, want output_invalid", att.ErrorClass)
	}
	// No result row may exist for a failed task.
	if _, err := rtStore.GetResultByTaskID(context.Background(), taskID); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("result row exists for failed task: %v", err)
	}
}

func TestIndeterminateBlocksWorkerUntilResolved(t *testing.T) {
	clearTasks(t)
	client := mustClient(t)
	policy := mustPolicy(t, "wpolicy", 3, nil)
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, policy.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000000"), payload)
	secondID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000001"), payload)

	w, _, _ := newWorkerEnv(t, "none", 0)
	// Replace the backend with one that fails indeterminate exactly once:
	// the first task blocks the worker, the second succeeds after operator
	// resolution.
	indet := &indeterminateOnceAdapter{}
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) { return indet, nil })
	startWorker(t, w)

	// The first task ends in the ambiguous state; the worker must block.
	waitForState(t, taskID, domain.TaskRunning, 10*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		blocked, err := rtStore.HasUnresolvedIndeterminate(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	blocked, err := rtStore.HasUnresolvedIndeterminate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("worker did not leave an unresolved indeterminate task")
	}

	// The second task stays queued: the worker refuses all new inference.
	time.Sleep(500 * time.Millisecond)
	second, err := rtStore.GetTask(context.Background(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != domain.TaskQueued {
		t.Errorf("second task state = %s, want queued while blocked", second.State)
	}

	// Operator resolution: requeue the ambiguous task with a future
	// availability (bounded operator backoff). The worker unblocks and the
	// second task completes; the requeued task stays ineligible.
	items, err := rtStore.ListUnresolvedRecovery(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery items = %d, err %v; want 1", len(items), err)
	}
	if _, err := rtStore.RequeueForRetry(context.Background(), taskID, items[0].AttemptNo,
		string(domain.ErrorIndeterminate), "operator_requeue",
		time.Now().Add(10*time.Minute)); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	waitForState(t, secondID, domain.TaskSucceeded, 10*time.Second)
	// The requeued task is queued but not eligible until its availability.
	requeued, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.State != domain.TaskQueued || requeued.RetryAvailableAt == nil {
		t.Errorf("requeued task state=%s retry_available_at=%v; want queued with backoff", requeued.State, requeued.RetryAvailableAt)
	}
}

func TestCancelDuringCallDiscardsOutput(t *testing.T) {
	client, _, taskID := newTask(t, 3, nil)
	w, _, _ := newWorkerEnv(t, "none", 400*time.Millisecond)
	startWorker(t, w)

	waitForState(t, taskID, domain.TaskRunning, 10*time.Second)
	if _, err := rtStore.RequestCancelRunning(context.Background(), taskID, client.ID); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	task := waitForState(t, taskID, domain.TaskCancelled, 10*time.Second)
	if task.FinishedAt == nil {
		t.Error("cancelled task missing finished_at")
	}
	if _, err := rtStore.GetResultByTaskID(context.Background(), taskID); !errors.Is(err, postgres.ErrNotFound) {
		t.Errorf("output persisted despite cancellation: %v", err)
	}
}

func TestCancelBeforeCallRunsNoInference(t *testing.T) {
	client, _, taskID := newTask(t, 3, nil)
	w, leader, _ := newWorkerEnv(t, "none", 5*time.Second)
	ctx := context.Background()
	if ok, err := leader.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("acquire leadership: %v, %v", ok, err)
	}
	defer leader.Release()

	res, err := w.sched.ClaimNext(ctx)
	if err != nil || res.NoTask {
		t.Fatalf("claim: %v no_task=%v", err, res.NoTask)
	}
	// Simulate the API recording cancel intent right after the claim.
	if _, err := rtStore.RequestCancelRunning(ctx, taskID, client.ID); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	// The before-call check reads the task row: re-fetch so the cancel
	// intent is visible, keeping the claimed payload.
	fresh, err := rtStore.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	claimed := res.Task
	claimed.Task = fresh
	// The fake would take 5s if called; a quick cancel proves no inference.
	start := time.Now()
	if err := w.execute(ctx, claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("execute took %s; adapter was invoked despite cancel intent", elapsed)
	}
	task, err := rtStore.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskCancelled {
		t.Errorf("state = %s, want cancelled", task.State)
	}
}

func TestLockLossStopsWorker(t *testing.T) {
	clearTasks(t)
	w, leader, _ := newWorkerEnv(t, "none", 0)
	startWorker(t, w)
	time.Sleep(300 * time.Millisecond)
	// Drop the lock while the worker is idle, then enqueue a task: it must
	// never be claimed after lock loss.
	leader.Release()
	time.Sleep(300 * time.Millisecond)

	client := mustClient(t)
	policy := mustPolicy(t, "wpolicy", 3, nil)
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, policy.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, policy.ID, uniqueName("wkey-0000000000000000"), payload)

	time.Sleep(1 * time.Second)
	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskQueued {
		t.Errorf("state = %s, want queued (no claims after lock loss)", task.State)
	}
}

func TestGracefulShutdownDrainsInFlight(t *testing.T) {
	_, _, taskID := newTask(t, 3, nil)
	w, _, _ := newWorkerEnv(t, "none", 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	defer cancel()

	waitForState(t, taskID, domain.TaskRunning, 10*time.Second)
	cancel() // stop claiming; the in-flight call must finish
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("worker run error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop")
	}
	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskSucceeded {
		t.Errorf("state = %s, want succeeded (drained)", task.State)
	}
}

func TestRetryDelayBounded(t *testing.T) {
	delays := []int{2, 10}
	for attempt := 1; attempt <= 3; attempt++ {
		d := retryDelay(delays, attempt, nil, time.Now)
		if d < time.Second || d > 16*time.Second {
			t.Errorf("attempt %d delay %s out of bounds", attempt, d)
		}
	}
	// Without a rand source there is no jitter.
	if d := retryDelay(delays, 1, nil, time.Now); d != 2*time.Second {
		t.Errorf("no-jitter delay = %s, want 2s", d)
	}
}

func TestDecodePayloadRejectsInvalid(t *testing.T) {
	if _, err := decodePayload([]byte(`{"messages":[]}`)); err == nil {
		t.Error("empty submission accepted")
	}
	if _, err := decodePayload([]byte(`not json`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestOperationalPreflightStopsUnmanagedLocalWorkload(t *testing.T) {
	_, workload, taskID := newTask(t, 3, nil)
	delete(testConfiguredWorkloads, workload.Name)

	spy := &countingAdapter{}
	w, leader, _ := newWorkerEnv(t, "none", 0)
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		return spy, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var operationalErr error
	select {
	case <-deadline.C:
		t.Fatal("worker did not enter operational fail-closed state")
	case <-ticker.C:
		operationalErr = w.OperationalFailure()
		if operationalErr == nil {
			for operationalErr == nil {
				select {
				case <-deadline.C:
					t.Fatal("worker did not enter operational fail-closed state")
				case <-ticker.C:
					operationalErr = w.OperationalFailure()
				}
			}
		}
	}
	if operationalErr == nil {
		t.Fatal("worker did not record operational failure")
	}
	if leader.Held() {
		t.Fatal("worker retained execution leadership after operational failure")
	}

	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskRunning {
		t.Fatalf("unmanaged task state = %s, want running claimed recovery", task.State)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != postgres.AttemptPhaseClaimed {
		t.Fatalf("unmanaged attempt state = %s, want claimed", attempt.State)
	}
	if spy.calls.Load() != 0 {
		t.Fatalf("unmanaged workload invoked adapter %d times", spy.calls.Load())
	}
	blocked, err := rtStore.HasUnresolvedIndeterminate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("safe claimed preflight failure incorrectly blocked indeterminate recovery")
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("worker shutdown after operational failure: %v", err)
	}
}

func TestH00WorkerPreflightRevocationCallsNoAdapter(t *testing.T) {
	client, policy, taskID := newTask(t, 3, nil)
	claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim revoked task: %v", err)
	}
	if claimed.ID != taskID {
		t.Fatalf("claimed task = %s, want %s", claimed.ID, taskID)
	}
	if err := adminStore.RevokeWorkloadBySlug(context.Background(), client.ID, policy.Name); err != nil {
		t.Fatalf("revoke workload before worker preflight: %v", err)
	}

	spy := &countingAdapter{}
	w, _, _ := newWorkerEnv(t, "none", 0)
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		return spy, nil
	})
	if err := w.execute(context.Background(), claimed); err != nil {
		t.Fatalf("revoked worker execute: %v", err)
	}
	if got := spy.calls.Load(); got != 0 {
		t.Fatalf("revoked worker invoked adapter %d times, want 0", got)
	}
	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read revoked task: %v", err)
	}
	if task.State != domain.TaskFailed {
		t.Fatalf("revoked task state = %s, want failed", task.State)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read revoked attempt: %v", err)
	}
	if attempt.State != postgres.AttemptPhaseFailed || attempt.ErrorClass == nil ||
		*attempt.ErrorClass != string(domain.ErrorPolicyRejected) || attempt.ErrorCode == nil ||
		*attempt.ErrorCode != "authorization_revoked" {
		t.Fatalf("revoked attempt = %+v, want policy_rejected/authorization_revoked", attempt)
	}
	if _, err := rtStore.GetResultByTaskID(context.Background(), taskID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("revoked task has a result: %v", err)
	}
}

func TestH00WorkerPreflightTopologyFailureCallsNoAdapter(t *testing.T) {
	_, workload, taskID := newTask(t, 3, nil)
	claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim topology-failure task: %v", err)
	}
	if claimed.ID != taskID {
		t.Fatalf("claimed task = %s, want %s", claimed.ID, taskID)
	}

	spy := &countingAdapter{}
	w, _, _ := newWorkerEnv(t, "none", 0)
	configured := make(map[string][32]byte, len(testConfiguredWorkloads))
	for slug, digest := range testConfiguredWorkloads {
		configured[slug] = digest
	}
	wrong := configured[workload.Name]
	wrong[0]++
	configured[workload.Name] = wrong
	w.configuredWorkloads = configured
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		return spy, nil
	})
	if err := w.execute(context.Background(), claimed); !errors.Is(err, postgres.ErrPreflightOperational) {
		t.Fatalf("topology-failure worker execute = %v, want operational preflight failure", err)
	}
	if got := spy.calls.Load(); got != 0 {
		t.Fatalf("topology-failure worker invoked adapter %d times, want 0", got)
	}
	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read topology-failure task: %v", err)
	}
	if task.State != domain.TaskRunning {
		t.Fatalf("topology-failure task state = %s, want running", task.State)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read topology-failure attempt: %v", err)
	}
	if attempt.State != postgres.AttemptPhaseClaimed {
		t.Fatalf("topology-failure attempt state = %s, want claimed", attempt.State)
	}
	if attempt.ErrorClass != nil || attempt.ErrorCode != nil {
		t.Fatalf("topology-failure attempt was terminalized: %+v", attempt)
	}
}

func TestH00WorkerAdapterRunsOnlyAfterAuthorizedCommit(t *testing.T) {
	_, _, taskID := newTask(t, 3, nil)
	claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim authorized-boundary task: %v", err)
	}
	if claimed.ID != taskID {
		t.Fatalf("claimed task = %s, want %s", claimed.ID, taskID)
	}

	var factoryState, inferState string
	spy := &boundaryAdapter{beforeCall: func() {
		attempt, err := rtStore.GetLastAttempt(context.Background(), taskID)
		if err != nil {
			t.Fatalf("read attempt at adapter invocation: %v", err)
		}
		inferState = attempt.State
	}}
	w, _, _ := newWorkerEnv(t, "none", 0)
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		attempt, err := rtStore.GetLastAttempt(context.Background(), taskID)
		if err != nil {
			t.Fatalf("read attempt at adapter construction: %v", err)
		}
		factoryState = attempt.State
		return spy, nil
	})
	if err := w.execute(context.Background(), claimed); err != nil {
		t.Fatalf("authorized-boundary worker execute: %v", err)
	}
	if factoryState != postgres.AttemptPhaseAuthorized || inferState != postgres.AttemptPhaseAuthorized {
		t.Fatalf("adapter boundary states: factory=%q infer=%q, want authorized/authorized", factoryState, inferState)
	}
	if got := spy.calls.Load(); got != 1 {
		t.Fatalf("authorized worker adapter calls = %d, want 1", got)
	}
	finalTask, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read authorized-boundary task: %v", err)
	}
	if finalTask.State != domain.TaskSucceeded {
		t.Fatalf("authorized-boundary task state = %s, want succeeded", finalTask.State)
	}
}
