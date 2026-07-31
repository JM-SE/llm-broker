package postgres

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

	"github.com/JM-SE/llm-broker/migrations"
)

// Integration tests require a disposable, dedicated PostgreSQL. Create it with
// scripts/pg-test-up.sh and export the two URLs it prints. Without them every
// test in this package is skipped (the package contains no unit-only tests).
var (
	adminStore *Store // owner/migration role (schema authority)
	rtStore    *Store // least-privilege runtime role (DML only)
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || runtimeURL == "" {
		fmt.Println("postgres integration tests skipped: set TEST_DATABASE_URL and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
		os.Exit(0)
	}

	ctx := context.Background()
	opts := Options{MaxConns: 8, ConnectTimeout: 5 * time.Second}

	var err error
	adminStore, err = Open(ctx, adminURL, opts)
	if err != nil {
		fmt.Printf("integration setup: admin connection: %v\n", err)
		os.Exit(1)
	}
	defer adminStore.Close()

	// Reset the schema so every run starts clean. Safe only because this
	// database is dedicated and disposable.
	if _, err := adminStore.pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		fmt.Printf("integration setup: reset schema: %v\n", err)
		os.Exit(1)
	}

	// Apply all migrations with the migration/owner role.
	std := stdlib.OpenDBFromPool(adminStore.pool)
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

	// Create (or reset) the least-privilege runtime role from the runtime URL.
	runtimeUser, runtimePassword, err := credentialsFromURL(runtimeURL)
	if err != nil {
		fmt.Printf("integration setup: runtime URL: %v\n", err)
		os.Exit(1)
	}
	roleIdent := pgx.Identifier{runtimeUser}.Sanitize()
	var exists bool
	if err := adminStore.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = $1)", runtimeUser).Scan(&exists); err != nil {
		fmt.Printf("integration setup: check role: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		if _, err := adminStore.pool.Exec(ctx,
			fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", roleIdent, runtimePassword)); err != nil {
			fmt.Printf("integration setup: create runtime role: %v\n", err)
			os.Exit(1)
		}
	}
	if _, err := adminStore.pool.Exec(ctx, fmt.Sprintf(
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
	if _, err := adminStore.pool.Exec(ctx, grants); err != nil {
		fmt.Printf("integration setup: grant runtime role: %v\n", err)
		os.Exit(1)
	}

	rtStore, err = Open(ctx, runtimeURL, opts)
	if err != nil {
		fmt.Printf("integration setup: runtime connection: %v\n", err)
		os.Exit(1)
	}
	defer rtStore.Close()

	os.Exit(m.Run())
}

// credentialsFromURL extracts user and password from a connection URL.
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

// fixtureSeq keeps fixture names unique across tests: the database persists
// for the whole test run, so fixed names would collide between tests.
var fixtureSeq atomic.Uint64

func testHash(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, fixtureSeq.Add(1))
}

func mustCreateClient(t *testing.T, s *Store, name string) Client {
	t.Helper()
	// The identifier mirrors the auth contract (12-char lookup prefix).
	identifier := fmt.Sprintf("fx%010d", fixtureSeq.Add(1))
	c, err := s.CreateClient(context.Background(), CreateClientParams{
		Name:                 uniqueName(name),
		CredentialIdentifier: identifier,
		CredentialDigest:     testHash(name[0]),
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create client %s: %v", name, err)
	}
	return c
}

func mustUpsertPolicy(t *testing.T, s *Store, name string) WorkloadPolicy {
	t.Helper()
	p, err := s.UpsertWorkloadPolicy(context.Background(), WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            "normal",
		MaxActiveTasks:           250,
		MaxAttempts:              3,
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

func mustCreateTask(t *testing.T, s *Store, clientID, workloadID int64, key string, hash []byte) CreateTaskResult {
	t.Helper()
	res, err := s.CreateTask(context.Background(), CreateTaskParams{
		ClientID:       clientID,
		WorkloadID:     workloadID,
		Capability:     "instruct",
		IdempotencyKey: key,
		RequestHash:    hash,
		Payload:        []byte(`{"messages":[]}`),
	})
	if err != nil {
		t.Fatalf("create task %s: %v", key, err)
	}
	return res
}

func mustClaim(t *testing.T, s *Store, workerID uuid.UUID) TaskWithPayload {
	t.Helper()
	tw, err := s.ClaimNextQueued(context.Background(), workerID, 60*time.Second)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	return tw
}
