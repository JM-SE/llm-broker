package scheduler

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

	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/migrations"
)

// Integration tests require a disposable, dedicated PostgreSQL (see
// scripts/pg-test-up.sh). Without the environment variables every test in
// this package is skipped.
var (
	adminStore *postgres.Store // schema authority (fixtures)
	rtStore    *postgres.Store // least-privilege runtime role (claims)
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || runtimeURL == "" {
		fmt.Println("scheduler integration tests skipped: set TEST_DATABASE_URL and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
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
	if err := provider.Close(); err != nil {
		fmt.Printf("integration setup: close provider: %v\n", err)
		os.Exit(1)
	}
	if err := std.Close(); err != nil {
		fmt.Printf("integration setup: close stdlib bridge: %v\n", err)
		os.Exit(1)
	}

	runtimeUser, runtimePassword, err := credentialsFromURL(runtimeURL)
	if err != nil {
		fmt.Printf("integration setup: runtime URL: %v\n", err)
		os.Exit(1)
	}
	roleIdent := pgx.Identifier{runtimeUser}.Sanitize()
	var exists bool
	if err := adminStore.Pool().QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = $1)", runtimeUser).Scan(&exists); err != nil {
		fmt.Printf("integration setup: check role: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		if _, err := adminStore.Pool().Exec(ctx,
			fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", roleIdent, runtimePassword)); err != nil {
			fmt.Printf("integration setup: create runtime role: %v\n", err)
			os.Exit(1)
		}
	}
	if _, err := adminStore.Pool().Exec(ctx, fmt.Sprintf(
		"ALTER ROLE %s PASSWORD '%s'; ALTER ROLE %s SET search_path = public",
		roleIdent, runtimePassword, roleIdent)); err != nil {
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

func credentialsFromURL(connURL string) (string, string, error) {
	u, err := url.Parse(connURL)
	if err != nil {
		return "", "", err
	}
	if u.User == nil {
		return "", "", fmt.Errorf("URL has no user info")
	}
	password, _ := u.User.Password()
	return u.User.Username(), password, nil
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
	p, err := adminStore.UpsertWorkloadPolicy(context.Background(), postgres.WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            class,
		MaxActiveTasks:           250,
		MaxAttempts:              maxAttempts,
		RetryDelaysSeconds:       []int{1, 5},
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
	if err := adminStore.GrantWorkload(context.Background(), clientID, workloadID); err != nil {
		t.Fatalf("grant workload %d to client %d: %v", workloadID, clientID, err)
	}
}

func mustEnqueue(t *testing.T, clientID, workloadID int64, capability, key string) uuid.UUID {
	t.Helper()
	if len(key) < 8 {
		key = uniqueName("taskkey")
	}
	res, err := adminStore.CreateTask(context.Background(), postgres.CreateTaskParams{
		ClientID:       clientID,
		WorkloadID:     workloadID,
		Capability:     capability,
		IdempotencyKey: key,
		RequestHash:    []byte("0123456789abcdef0123456789abcdef"),
		Payload:        []byte(`{"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("enqueue task %s: %v", key, err)
	}
	return res.TaskID
}

// backdateTask rewinds created_at so aging sees the given queued duration.
func backdateTask(t *testing.T, taskID uuid.UUID, seconds int64) {
	t.Helper()
	if _, err := adminStore.Pool().Exec(context.Background(), `
		UPDATE tasks SET created_at = now() - ($2 * interval '1 second')
		WHERE id = $1`, taskID, seconds); err != nil {
		t.Fatalf("backdate task %s: %v", taskID, err)
	}
}

// clearQueued removes leftover queued tasks so tests observe only their own
// fixtures (the claim order is global).
func clearQueued(t *testing.T) {
	t.Helper()
	if _, err := adminStore.Pool().Exec(context.Background(),
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
