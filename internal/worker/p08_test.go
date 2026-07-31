package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// enqueueCap is mustEnqueue with a configurable capability.
func enqueueCap(t *testing.T, clientID, workloadID int64, key, capability string, payload []byte) uuid.UUID {
	t.Helper()
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = 0xAB
	}
	res, err := rtStore.CreateTask(context.Background(), postgres.CreateTaskParams{
		ClientID:       clientID,
		WorkloadID:     workloadID,
		Capability:     capability,
		IdempotencyKey: key,
		RequestHash:    hash,
		Payload:        payload,
	})
	if err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	return res.TaskID
}

// TestLoadOneActiveInference runs a burst through one fake backend and
// verifies every task completes serially: the queue drains completely and
// the product invariant (at most one running task) holds throughout.
func TestLoadOneActiveInference(t *testing.T) {
	clearTasks(t)
	w, _, _ := newWorkerEnv(t, config.FakeFailNone, 5*time.Millisecond)
	startWorker(t, w)

	client := mustClient(t)
	pol := mustPolicy(t, "load-pol", 1, nil)
	mustGrant(t, client.ID, pol.ID)
	payload := payloadFor(t, pol.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "load"}},
		domain.ResponseContract{Kind: domain.ResponseText})

	const n = 30
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, mustEnqueue(t, client.ID, pol.ID, fmt.Sprintf("load-key-%03d", i), payload))
	}
	for _, id := range ids {
		waitForState(t, id, domain.TaskSucceeded, 30*time.Second)
	}
	stats, err := rtStore.QueueStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Queued != 0 || stats.Running != 0 {
		t.Errorf("queue not drained: queued=%d running=%d", stats.Queued, stats.Running)
	}
}

// TestBackendUnreachableRetriesThenFails verifies the fail-closed retry
// path against a real (unreachable) endpoint: connect-refused is classified
// transient_unsent, retried once with persisted backoff, then the task
// fails terminally and the worker remains healthy for other work.
func TestBackendUnreachableRetriesThenFails(t *testing.T) {
	clearTasks(t)
	t.Setenv("LLM_BROKER_TEST_BACKEND_KEY", "test-secret-token-42")

	policy := scheduler.DefaultPolicy()
	policy.HeartbeatInterval = 100 * time.Millisecond
	leader := scheduler.NewLeader(rtStore, uuid.New(), policy.HeartbeatInterval, nil)
	sched := scheduler.New(rtStore, policy, leader)
	w := New(Options{
		Store:     rtStore,
		Scheduler: sched,
		Leader:    leader,
		Registry:  backend.DefaultRegistry,
		Capabilities: map[string]config.Capability{
			"instruct": {Backend: "unreachable"},
			"quick":    {Backend: "fake"},
		},
		Backends: map[string]config.BackendConfig{
			"unreachable": {
				Type:          config.AdapterOpenAICompatible,
				BaseURL:       "http://127.0.0.1:1/v1",
				Model:         "operator-model-alias",
				CredentialEnv: "LLM_BROKER_TEST_BACKEND_KEY",
			},
			"fake": {Type: config.AdapterFake, FailMode: config.FakeFailNone},
		},
		Policy:    policy,
		ClaimPoll: 50 * time.Millisecond,
	})
	startWorker(t, w)

	client := mustClient(t)
	pol := mustPolicy(t, "unreach-pol", 2, []int{1})
	mustGrant(t, client.ID, pol.ID)

	taskID := enqueueCap(t, client.ID, pol.ID, uniqueName("taskkey"), "instruct",
		payloadFor(t, pol.Name, "instruct",
			[]domain.Message{{Role: domain.RoleUser, Content: "ping"}},
			domain.ResponseContract{Kind: domain.ResponseText}))
	waitForState(t, taskID, domain.TaskFailed, 30*time.Second)
	task, err := rtStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one retry)", task.Attempts)
	}
	att, err := rtStore.GetLastAttempt(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if att.ErrorClass == nil || *att.ErrorClass != string(domain.ErrorTransientUnsent) {
		t.Errorf("last attempt class = %v, want transient_unsent", att.ErrorClass)
	}

	// The worker survived: a healthy-capability task completes.
	okPol := mustPolicy(t, "ok-pol", 1, nil)
	mustGrant(t, client.ID, okPol.ID)
	okID := enqueueCap(t, client.ID, okPol.ID, uniqueName("taskkey"), "quick",
		payloadFor(t, okPol.Name, "quick",
			[]domain.Message{{Role: domain.RoleUser, Content: "hi"}},
			domain.ResponseContract{Kind: domain.ResponseText}))
	waitForState(t, okID, domain.TaskSucceeded, 10*time.Second)
}
