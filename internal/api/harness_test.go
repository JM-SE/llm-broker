package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/migrations"
)

// Integration tests require the disposable PostgreSQL from
// scripts/pg-test-up.sh (TEST_DATABASE_URL + TEST_DATABASE_RUNTIME_URL).
var (
	adminStore *postgres.Store // migration/owner role: fixtures
	rtStore    *postgres.Store // least-privilege runtime role: server under test
)

func TestMain(m *testing.M) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	runtimeURL := os.Getenv("TEST_DATABASE_RUNTIME_URL")
	if adminURL == "" || runtimeURL == "" {
		fmt.Println("api integration tests skipped: set TEST_DATABASE_URL and TEST_DATABASE_RUNTIME_URL (see scripts/pg-test-up.sh)")
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
	_ = provider.Close()

	user, password, err := credentialsFromURL(runtimeURL)
	if err != nil {
		fmt.Printf("integration setup: runtime URL: %v\n", err)
		os.Exit(1)
	}
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
	_ = std.Close()

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

// --- fixtures --------------------------------------------------------------

var fixtureSeq atomic.Uint64

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, fixtureSeq.Add(1))
}

// testConfig builds the operator config used by the server under test.
// Capabilities come from config; workload policies live in the database.
func testConfig() *config.Config {
	return &config.Config{
		Version: 1,
		API:     config.APIConfig{Listen: "127.0.0.1:0", BodyLimitBytes: 524288},
		Capabilities: map[string]config.Capability{
			"instruct": {Backend: "fake"},
		},
		Workloads: map[string]config.WorkloadPolicy{},
	}
}

// testAdminURL returns the disposable admin database URL for ad-hoc stores.
func testAdminURL() string { return os.Getenv("TEST_DATABASE_URL") }

// discardLogger returns a logger that writes nowhere.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// testEnv is a server under test plus its captured logs.
type testEnv struct {
	ts    *httptest.Server
	logs  *bytes.Buffer
	store *postgres.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := rtStore.GetClientByIdentifier(ctx, identifier)
		if errors.Is(err, postgres.ErrNotFound) {
			return auth.Identity{}, nil, false, nil
		}
		if err != nil {
			return auth.Identity{}, nil, false, err
		}
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})
	srv := New(Options{Store: rtStore, Config: testConfig(), Verifier: verifier, Logger: logger})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &testEnv{ts: ts, logs: &logs, store: rtStore}
}

// newClient provisions a client with a real credential.
func newClient(t *testing.T, name string) (auth.Token, int64) {
	t.Helper()
	tok, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, err := adminStore.CreateClient(context.Background(), postgres.CreateClientParams{
		Name:                 uniqueName(name),
		CredentialIdentifier: tok.Identifier,
		CredentialDigest:     tok.Digest[:],
		Enabled:              true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return tok, c.ID
}

// clientIDOf resolves the client id for a token via the admin store.
func clientIDOf(t *testing.T, tok auth.Token) int64 {
	t.Helper()
	c, err := adminStore.GetClientByIdentifier(context.Background(), tok.Identifier)
	if err != nil {
		t.Fatalf("lookup client: %v", err)
	}
	return c.ID
}

// grantWorkload creates a workload policy and grants it to the client.
// Returns the workload name.
func grantWorkload(t *testing.T, clientID int64, maxActive int) string {
	t.Helper()
	name := uniqueName("wl")
	_, err := adminStore.UpsertWorkloadPolicy(context.Background(), postgres.WorkloadPolicy{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           maxActive,
		MaxAttempts:              3,
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
		t.Fatalf("upsert policy: %v", err)
	}
	c, err := adminStore.GetClientByID(context.Background(), clientID)
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	pol, err := adminStore.GetWorkloadPolicyByName(context.Background(), name)
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if err := adminStore.GrantWorkload(context.Background(), c.ID, pol.ID); err != nil {
		t.Fatalf("grant workload: %v", err)
	}
	return name
}

// doReq performs one HTTP request against the test server.
func doReq(t *testing.T, ts *httptest.Server, method, path, token, key, prefer, body string) (*http.Response, []byte) {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reqBody)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func submission(workload string) string {
	return fmt.Sprintf(`{"workload": %q, "capability": "instruct",
		"messages": [{"role": "user", "content": "hi"}],
		"response": {"kind": "text"}}`, workload)
}

// decodeProblem decodes a problem+json body.
func decodeProblem(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}
	return p
}
