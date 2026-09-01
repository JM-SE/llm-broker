package scheduler

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

	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
	"github.com/JM-SE/llm-broker/migrations"
)

// Integration tests require a disposable, dedicated PostgreSQL (see
// scripts/pg-test-up.sh). Without the environment variables every test in
// this package is skipped.
var (
	adminStore *postgres.Store // schema authority (fixtures)
	adminDB    *sql.DB
	rtStore    *postgres.Store // least-privilege runtime role (claims)
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	provisionerURL := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || provisionerURL == "" || runtimeURL == "" {
		if os.Getenv("LLM_BROKER_REQUIRE_TEST_DATABASE") == "1" {
			fmt.Println("scheduler integration tests require TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL")
			os.Exit(1)
		}
		fmt.Println("scheduler integration tests skipped: set TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
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
	if err := provider.Close(); err != nil {
		fmt.Printf("integration setup: close provider: %v\n", err)
		os.Exit(1)
	}
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

// --- shared fixtures -------------------------------------------------------

var fixtureSeq atomic.Uint64

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, fixtureSeq.Add(1))
}

func mustClient(t *testing.T, name string) postgres.Client {
	t.Helper()
	identifier := fmt.Sprintf("fx%010d", fixtureSeq.Add(1))
	c, err := adminStore.CreateClient(context.Background(), postgres.CreateClientParams{
		Name:                 uniqueName(name),
		CredentialIdentifier: identifier,
		CredentialDigest:     []byte("0123456789abcdef0123456789abcdef"),
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create client %s: %v", name, err)
	}
	return c
}

func mustPolicy(t *testing.T, name, class string, maxAttempts int) postgres.WorkloadPolicy {
	t.Helper()
	p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            class,
		MaxActiveTasks:           250,
		MaxAttempts:              maxAttempts,
		RetryDelaysSeconds:       []int{1, 5},
		WorkloadTimeoutSeconds:   300,
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
	})
	if err != nil {
		t.Fatalf("upsert policy %s: %v", name, err)
	}
	return p
}

func mustGrant(t *testing.T, clientID, workloadID int64) {
	t.Helper()
	workload, err := adminStore.GetWorkloadPolicyByID(context.Background(), workloadID)
	if err != nil {
		t.Fatalf("get workload %d: %v", workloadID, err)
	}
	if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, workload.Name); err != nil {
		t.Fatalf("grant workload %d to client %d: %v", workloadID, clientID, err)
	}
}

func mustEnqueue(t *testing.T, clientID, workloadID int64, capability, key string) uuid.UUID {
	t.Helper()
	if len(key) < 8 {
		key = uniqueName("taskkey")
	}
	var taskID uuid.UUID
	err := adminDB.QueryRowContext(context.Background(), `
		INSERT INTO tasks (client_id, workload_id, capability, idempotency_key, request_hash)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		clientID, workloadID, capability, key,
		[]byte("0123456789abcdef0123456789abcdef")).Scan(&taskID)
	if err != nil {
		t.Fatalf("enqueue task %s: %v", key, err)
	}
	if _, err := adminDB.ExecContext(context.Background(),
		`INSERT INTO task_payloads (task_id, payload) VALUES ($1,$2)`, taskID, `{"messages":[]}`); err != nil {
		t.Fatalf("insert task payload %s: %v", key, err)
	}
	return taskID
}

// backdateTask rewinds created_at so aging sees the given queued duration.
func backdateTask(t *testing.T, taskID uuid.UUID, seconds int64) {
	t.Helper()
	if _, err := adminDB.ExecContext(context.Background(), `
		UPDATE tasks SET created_at = now() - ($2 * interval '1 second')
		WHERE id = $1`, taskID, seconds); err != nil {
		t.Fatalf("backdate task %s: %v", taskID, err)
	}
}

// clearQueued removes leftover queued tasks so tests observe only their own
// fixtures (the claim order is global).
func clearQueued(t *testing.T) {
	t.Helper()
	if _, err := adminDB.ExecContext(context.Background(),
		"DELETE FROM tasks WHERE state = 'queued'"); err != nil {
		t.Fatalf("clear queued tasks: %v", err)
	}
}

// newScheduler builds a leader + scheduler over the runtime-role store.
func newScheduler(t *testing.T) (*Leader, *Scheduler) {
	t.Helper()
	leader := NewLeader(rtStore, uuid.New(), 0, nil)
	ok, err := leader.TryAcquire(context.Background())
	if err != nil {
		t.Fatalf("acquire leadership: %v", err)
	}
	if !ok {
		t.Fatal("could not acquire leadership (another test holds it?)")
	}
	t.Cleanup(leader.Release)
	return leader, New(rtStore, DefaultPolicy(), leader)
}
