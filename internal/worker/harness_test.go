package worker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/migrations"

	// Adapter registration for the test binary.
	_ "github.com/JM-SE/llm-broker/internal/backend/fake"
	_ "github.com/JM-SE/llm-broker/internal/backend/openaicompat"
)

// Integration tests require a disposable, dedicated PostgreSQL. Create it with
// scripts/pg-test-up.sh and export the two URLs it prints. Without them every
// test in this package is skipped.
var (
	adminStore *postgres.Store
	rtStore    *postgres.Store
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || runtimeURL == "" {
		fmt.Println("worker integration tests skipped: set TEST_DATABASE_URL and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
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
	if _, err := adminStore.Pool().Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		fmt.Printf("integration setup: reset schema: %v\n", err)
		os.Exit(1)
	}
	std := stdlib.OpenDBFromPool(adminStore.Pool())
	provider, err := goose.NewProvider(goose.DialectPostgres, std, migrations.FS)
	if err != nil {
		fmt.Printf("integration setup: migration provider: %v\n", err)
		os.Exit(1)
	}
	if _, err := provider.Up(ctx); err != nil {
		fmt.Printf("integration setup: migrate up: %v\n", err)
		os.Exit(1)
	}
	_ = provider.Close()
	_ = std.Close()

	u, err := url.Parse(runtimeURL)
	if err != nil {
		fmt.Printf("integration setup: runtime URL: %v\n", err)
		os.Exit(1)
	}
	user := u.User.Username()
	password, _ := u.User.Password()
	roleIdent := pgx.Identifier{user}.Sanitize()
	var exists bool
	if err := adminStore.Pool().QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = $1)", user).Scan(&exists); err != nil {
		fmt.Printf("integration setup: check role: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		if _, err := adminStore.Pool().Exec(ctx,
			fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", roleIdent, password)); err != nil {
			fmt.Printf("integration setup: create runtime role: %v\n", err)
			os.Exit(1)
		}
	}
	if _, err := adminStore.Pool().Exec(ctx, fmt.Sprintf(
		"ALTER ROLE %s PASSWORD '%s'; ALTER ROLE %s SET search_path = public",
		roleIdent, password, roleIdent)); err != nil {
		fmt.Printf("integration setup: configure runtime role: %v\n", err)
		os.Exit(1)
	}
	grants := fmt.Sprintf(`
		GRANT USAGE ON SCHEMA public TO %s;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s;`,
		roleIdent, roleIdent, roleIdent)
	if _, err := adminStore.Pool().Exec(ctx, grants); err != nil {
		fmt.Printf("integration setup: grant runtime role: %v\n", err)
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
	c, err := rtStore.CreateClient(context.Background(), postgres.CreateClientParams{
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
	t.Helper()
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if retryDelays == nil {
		retryDelays = []int{1, 5}
	}
	p, err := rtStore.UpsertWorkloadPolicy(context.Background(), postgres.WorkloadPolicy{
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
	})
	if err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	return p
}

func mustGrant(t *testing.T, clientID, workloadID int64) {
	t.Helper()
	if err := rtStore.GrantWorkload(context.Background(), clientID, workloadID); err != nil {
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
	res, err := rtStore.CreateTask(context.Background(), postgres.CreateTaskParams{
		ClientID:       clientID,
		WorkloadID:     workloadID,
		Capability:     "instruct",
		IdempotencyKey: key,
		RequestHash:    hash,
		Payload:        payload,
	})
	if err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	return res.TaskID
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
	if _, err := rtStore.Pool().Exec(context.Background(),
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
		Backends:  map[string]config.BackendConfig{"fake": cfg},
		Policy:    policy,
		Now:       time.Now,
		ClaimPoll: 50 * time.Millisecond,
	})
	return w, leader, policy
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
