package worker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

func waitH00C3Worker(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("worker operational condition was not observed")
		case <-ticker.C:
		}
	}
}

func TestH00C3WorkerLocalConfigurationFailureStopsAllFurtherClaims(t *testing.T) {
	clearTasks(t)
	client, policy, firstID := newTask(t, 3, nil)
	secondID := mustEnqueue(t, client.ID, policy.ID, uniqueName("h00-c3-second-key"),
		payloadFor(t, policy.Name, "instruct", []domain.Message{{Role: domain.RoleUser, Content: "second"}}, textResponse))

	spy := &countingAdapter{}
	w, leader, _ := newWorkerEnv(t, "none", 0)
	w.registry = backend.NewRegistry()
	w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
		return spy, nil
	})
	wrong := w.configuredWorkloads[policy.Name]
	wrong[0]++
	w.configuredWorkloads[policy.Name] = wrong
	startWorker(t, w)

	waitH00C3Worker(t, func() bool {
		return w.OperationalFailure() != nil && !leader.Held()
	})
	first, err := rtStore.GetTask(context.Background(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := rtStore.GetTask(context.Background(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != domain.TaskRunning || second.State != domain.TaskQueued {
		t.Fatalf("worker failure states = first %s/second %s, want running/queued", first.State, second.State)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != postgres.AttemptPhaseClaimed || attempt.ErrorClass != nil || attempt.ErrorCode != nil {
		t.Fatalf("local configuration failure terminalized first attempt: %+v", attempt)
	}
	if _, err := rtStore.GetResultByTaskID(context.Background(), firstID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("local configuration failure produced result: %v", err)
	}
	if got := spy.calls.Load(); got != 0 {
		t.Fatalf("local configuration failure invoked adapter %d times", got)
	}
}

func TestH00C3WorkerDatabaseReadFailureReleasesLeadershipWithoutMutation(t *testing.T) {
	client, workload, firstID := newTask(t, 3, nil)
	secondID := mustEnqueue(t, client.ID, workload.ID, uniqueName("h00-c3-db-read-second"),
		payloadFor(t, workload.Name, "instruct", []domain.Message{{Role: domain.RoleUser, Content: "second"}}, textResponse))
	store, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_RUNTIME_URL"), postgres.Options{
		MaxConns: 2, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := scheduler.DefaultPolicy()
	policy.HeartbeatInterval = 100 * time.Millisecond
	leader := scheduler.NewLeader(store, uuid.New(), policy.HeartbeatInterval, nil)
	sched := scheduler.New(store, policy, leader)
	w := New(Options{
		Store: store, Scheduler: sched, Leader: leader, Registry: backend.DefaultRegistry,
		Capabilities:        map[string]config.Capability{"instruct": {Backend: "fake"}},
		Backends:            map[string]config.BackendConfig{"fake": {Type: config.AdapterFake}},
		ConfiguredWorkloads: map[string][32]byte{"h00-c3-db-read": testTopologyDigest()},
		Policy:              policy,
	})
	if ok, err := leader.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("acquire leadership = %t, %v", ok, err)
	}
	// Close the pool only after leadership has been acquired. The next
	// operational read therefore fails before a claim, while Run's normal
	// leadership owner remains observable through the private leader seam.
	store.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- w.loop(context.Background()) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("database read failure returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("database read failure did not stop the claim loop")
	}
	leader.Release()
	if leader.Held() {
		t.Fatal("leadership remained held after database read failure")
	}
	if w.OperationalFailure() != nil {
		t.Fatal("database read failure was misclassified as durable policy failure")
	}
	for _, id := range []uuid.UUID{firstID, secondID} {
		task, err := rtStore.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.State != domain.TaskQueued {
			t.Fatalf("database read failure claimed task %s: state=%s", id, task.State)
		}
		if _, err := rtStore.GetLastAttempt(context.Background(), id); !errors.Is(err, postgres.ErrNotFound) {
			t.Fatalf("database read failure created attempt for %s: %v", id, err)
		}
		if _, err := rtStore.GetResultByTaskID(context.Background(), id); !errors.Is(err, postgres.ErrNotFound) {
			t.Fatalf("database read failure produced result for %s: %v", id, err)
		}
	}
}
