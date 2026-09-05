package postgres

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/JM-SE/llm-broker/migrations"
)

// Integration tests require a disposable, dedicated PostgreSQL. Create it with
// scripts/pg-test-up.sh and export the two URLs it prints. Without them every
// test in this package is skipped (the package contains no unit-only tests).
var (
	adminStore       *Store // owner/migration role (schema authority)
	provisionerStore *Store // policy/client/grant provisioning role
	rtStore          *Store // least-privilege runtime role (DML only)
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	provisionerURL := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || provisionerURL == "" || runtimeURL == "" {
		if os.Getenv("LLM_BROKER_REQUIRE_TEST_DATABASE") == "1" {
			fmt.Println("postgres integration tests require TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL")
			os.Exit(1)
		}
		fmt.Println("postgres integration tests skipped: set TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
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

	rtStore, err = Open(ctx, runtimeURL, opts)
	if err != nil {
		fmt.Printf("integration setup: runtime connection: %v\n", err)
		os.Exit(1)
	}
	defer rtStore.Close()
	provisionerStore, err = Open(ctx, provisionerURL, opts)
	if err != nil {
		fmt.Printf("integration setup: provisioner connection: %v\n", err)
		os.Exit(1)
	}
	defer provisionerStore.Close()

	os.Exit(m.Run())
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

func applyPolicy(t *testing.T, s *Store, input WorkloadPolicy) WorkloadPolicy {
	t.Helper()
	var digest [32]byte
	if len(input.TopologyDigest) == len(digest) {
		copy(digest[:], input.TopologyDigest)
	}
	_, err := s.ApplyPolicies(context.Background(), []policy.Workload{{
		Name: input.Name, PriorityClass: input.PriorityClass, MaxActiveTasks: input.MaxActiveTasks,
		MaxAttempts: input.MaxAttempts, RetryDelaysSeconds: input.RetryDelaysSeconds,
		MaxOutputTokens: input.MaxOutputTokens, MaxOutputBytes: input.MaxOutputBytes,
		MaxOutputBytesExplicit: input.MaxOutputBytesExplicit, AllowedGeneration: input.AllowedGeneration,
		RequiredFeatures: input.RequiredFeatures, WorkloadTimeoutSeconds: input.WorkloadTimeoutSeconds,
		PayloadRetentionSeconds: input.PayloadRetentionSeconds, ResultRetentionSeconds: input.ResultRetentionSeconds,
		MetadataRetentionSeconds: input.MetadataRetentionSeconds, AllowedCapabilities: input.AllowedCapabilities,
		TopologyDigest: digest,
	}})
	if err != nil {
		t.Fatalf("apply policy %s: %v", input.Name, err)
	}
	p, err := s.GetWorkloadPolicyByName(context.Background(), input.Name)
	if err != nil {
		t.Fatalf("read applied policy %s: %v", input.Name, err)
	}
	return p
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
	input := WorkloadPolicy{
		Name:                     uniqueName(name),
		PriorityClass:            "normal",
		MaxActiveTasks:           250,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1, 5},
		WorkloadTimeoutSeconds:   300,
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
	}
	return applyPolicy(t, s, input)
}

func mustCreateTask(t *testing.T, s *Store, clientID, workloadID int64, key string, hash []byte) CreateTaskResult {
	t.Helper()
	res, err := s.createTaskFixture(context.Background(), CreateTaskParams{
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
