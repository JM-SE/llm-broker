package worker

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
	"github.com/JM-SE/llm-broker/migrations"

	// Adapter registration for the test binary.
	_ "github.com/JM-SE/llm-broker/internal/backend/fake"
	_ "github.com/JM-SE/llm-broker/internal/backend/openaicompat"
)

// Integration tests require a disposable, dedicated PostgreSQL. Create it with
// scripts/pg-test-up.sh and export the two URLs it prints. Without them every
// test in this package is skipped.
var (
	adminStore              *postgres.Store
	adminDB                 *sql.DB
	rtStore                 *postgres.Store
	testConfiguredWorkloads = map[string][32]byte{}
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	provisionerURL := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || provisionerURL == "" || runtimeURL == "" {
		if os.Getenv("LLM_BROKER_REQUIRE_TEST_DATABASE") == "1" {
			fmt.Println("worker integration tests require TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL")
			os.Exit(1)
		}
		fmt.Println("worker integration tests skipped: set TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
		os.Exit(0)
	}
	ctx := context.Background()
	opts := postgres.Options{MaxConns: 8, ConnectTimeout: 5 * time.Second}
	var err error
	adminStore, err = postgres.Open(ctx, adminURL, opts)
	if err != nil {
		fmt.Printf("integration setup: admin connection: %v\n", err)
		os.Exit(1)
	}
	defer adminStore.Close()
	if err := adminStore.Ping(ctx); err != nil {
		fmt.Printf("integration setup: ping: %v\n", err)
		os.Exit(1)
	}
	adminDB, err = testsupport.OpenDatabase(adminURL)
	if err != nil {
		fmt.Printf("integration setup: admin SQL bridge: %v\n", err)
		os.Exit(1)
	}
	if _, err := adminDB.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		fmt.Printf("integration setup: reset schema: %v\n", err)
		os.Exit(1)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, adminDB, migrations.FS)
	if err != nil {
		fmt.Printf("integration setup: migration provider: %v\n", err)
		os.Exit(1)
	}
	if _, err := provider.Up(ctx); err != nil {
		fmt.Printf("integration setup: migrate up: %v\n", err)
		os.Exit(1)
	}
	_ = provider.Close()
	// Goose closes the database handle passed to the provider. Reopen the
	// owner bridge for fixture-only DDL/DML after migrations complete.
	adminDB, err = testsupport.OpenDatabase(adminURL)
	if err != nil {
		fmt.Printf("integration setup: reopen admin SQL bridge: %v\n", err)
		os.Exit(1)
	}

	rtStore, err = postgres.Open(ctx, runtimeURL, opts)
	if err != nil {
		fmt.Printf("integration setup: runtime connection: %v\n", err)
		os.Exit(1)
	}
	defer rtStore.Close()
	os.Exit(m.Run())
}

// --- fixtures --------------------------------------------------------------

var fixtureSeq atomic.Uint64

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, fixtureSeq.Add(1))
}

func mustClient(t *testing.T) postgres.Client {
	t.Helper()
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = 0xCD
	}
	c, err := adminStore.CreateClient(context.Background(), postgres.CreateClientParams{
		Name:                 uniqueName("wclient"),
		CredentialIdentifier: fmt.Sprintf("fx%010d", fixtureSeq.Add(1)),
		CredentialDigest:     digest,
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return c
}

// mustPolicy creates a workload policy; retryDelays of nil means [1,5].
func mustPolicy(t *testing.T, name string, maxAttempts int, retryDelays []int) postgres.WorkloadPolicy {
	return mustPolicyWithDigest(t, name, maxAttempts, retryDelays, testTopologyDigest())
}

func mustPolicyWithDigest(t *testing.T, name string, maxAttempts int, retryDelays []int, digest [32]byte) postgres.WorkloadPolicy {
	t.Helper()
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if retryDelays == nil {
		retryDelays = []int{1, 5}
	}
	p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            "normal",
		MaxActiveTasks:           250,
		MaxAttempts:              maxAttempts,
		RetryDelaysSeconds:       retryDelays,
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		WorkloadTimeoutSeconds:   300,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      []string{"instruct"},
		TopologyDigest:           digest[:],
	})
	if err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	testConfiguredWorkloads[p.Name] = digest
	return p
}

func mustGrant(t *testing.T, clientID, workloadID int64) {
	t.Helper()
	workload, err := adminStore.GetWorkloadPolicyByID(context.Background(), workloadID)
	if err != nil {
		t.Fatalf("get workload %d: %v", workloadID, err)
	}
	if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, workload.Name); err != nil {
		t.Fatalf("grant workload: %v", err)
	}
}

// payloadFor builds the canonical submission bytes the API would store.
func payloadFor(t *testing.T, workload, capability string, messages []domain.Message, rc domain.ResponseContract) []byte {
	t.Helper()
	sub := domain.TaskSubmission{
		Workload:   workload,
		Capability: capability,
		Messages:   messages,
		Response:   rc,
	}
	b, err := sub.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize submission: %v", err)
	}
	return b
}

func mustEnqueue(t *testing.T, clientID, workloadID int64, key string, payload []byte) uuid.UUID {
	t.Helper()
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = 0xAB
	}
	var taskID uuid.UUID
	err := adminDB.QueryRowContext(context.Background(), `
		INSERT INTO tasks (client_id, workload_id, capability, idempotency_key, request_hash)
		VALUES ($1,$2,'instruct',$3,$4) RETURNING id`,
		clientID, workloadID, key, hash).Scan(&taskID)
	if err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	if _, err := adminDB.ExecContext(context.Background(),
		`INSERT INTO task_payloads (task_id, payload) VALUES ($1,$2)`, taskID, string(payload)); err != nil {
		t.Fatalf("insert task payload: %v", err)
	}
	return taskID
}

func waitForState(t *testing.T, taskID uuid.UUID, want domain.TaskState, timeout time.Duration) postgres.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, err := rtStore.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.State == want {
			return task
		}
		if task.State.Terminal() && task.State != want {
			t.Fatalf("task %s reached %s, want %s", taskID, task.State, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s within %s", taskID, want, timeout)
	return postgres.Task{}
}

// clearTasks removes queued and running tasks so one test's leftovers
// (notably unresolved indeterminate executions) cannot block the next test.
func clearTasks(t *testing.T) {
	t.Helper()
	if _, err := adminDB.ExecContext(context.Background(),
		`DELETE FROM tasks WHERE state IN ('queued', 'running')`); err != nil {
		t.Fatalf("clear tasks: %v", err)
	}
}

// newWorkerEnv builds a worker over the runtime store with a fake backend in
// the given fail mode and delay, plus its leader. Tests own the leader
// lifecycle (Release) and the run context.
func newWorkerEnv(t *testing.T, failMode string, delay time.Duration) (*Worker, *scheduler.Leader, scheduler.Policy) {
	t.Helper()
	policy := scheduler.DefaultPolicy()
	policy.HeartbeatInterval = 100 * time.Millisecond
	workerID := uuid.New()
	leader := scheduler.NewLeader(rtStore, workerID, policy.HeartbeatInterval, nil)
	sched := scheduler.New(rtStore, policy, leader)
	cfg := config.BackendConfig{
		Type:     config.AdapterFake,
		FailMode: failMode,
		Delay:    config.Duration{Duration: delay},
	}
	w := New(Options{
		Store:     rtStore,
		Scheduler: sched,
		Leader:    leader,
		Registry:  backend.DefaultRegistry,
		Capabilities: map[string]config.Capability{
			"instruct": {Backend: "fake"},
		},
		Backends:            map[string]config.BackendConfig{"fake": cfg},
		ConfiguredWorkloads: testConfiguredWorkloads,
		Policy:              policy,
		Now:                 time.Now,
		ClaimPoll:           50 * time.Millisecond,
	})
	return w, leader, policy
}

func testTopologyDigest() [32]byte {
	cfg := &config.Config{
		Version:      2,
		Capabilities: map[string]config.Capability{"instruct": {Backend: "fake"}},
		Backends:     map[string]config.BackendConfig{"fake": {Type: config.AdapterFake, FailMode: config.FakeFailNone}},
	}
	return cfg.TopologyDigest()
}

// startWorker runs the worker loop and returns the cancel function.
func startWorker(t *testing.T, w *Worker) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Logf("worker run ended with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Log("worker did not stop within 5s")
		}
	})
	return cancel
}
