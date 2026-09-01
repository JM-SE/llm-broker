package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	brokerapi "github.com/JM-SE/llm-broker/internal/api"
	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
	"github.com/JM-SE/llm-broker/migrations"
)

type h00C35Snapshot struct {
	version      int64
	policies     string
	capabilities string
	grants       string
	tasks        string
	payloads     string
	attempts     string
	results      string
}

func h00C35CaptureSnapshot(t *testing.T) h00C35Snapshot {
	t.Helper()
	db := h00C35AdminDB(t)
	version := h00C35Version(t, db)
	capabilities := "<legacy schema: capability projection absent>"
	if version >= 4 {
		capabilities = h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT workload_id::text || ':' || capability AS order_key, c.*
			      FROM workload_policy_capabilities c) x`)
	}
	return h00C35Snapshot{
		version: version,
		policies: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT id::text AS order_key, p.* FROM workload_policies p) x`),
		capabilities: capabilities,
		grants: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT client_id::text || ':' || workload_id::text AS order_key, g.*
			      FROM client_workload_grants g) x`),
		tasks: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT id::text AS order_key, t.* FROM tasks t) x`),
		payloads: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT task_id::text AS order_key, p.* FROM task_payloads p) x`),
		attempts: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT id::text AS order_key, a.* FROM task_attempts a) x`),
		results: h00C35JSON(t, db, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT task_id::text AS order_key, r.* FROM task_results r) x`),
	}
}

func h00C35AdminDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := testsupport.OpenDatabase(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func h00C35Version(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func h00C35JSON(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func h00C35ResetSchema(t *testing.T, version int64) {
	t.Helper()
	db := h00C35AdminDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, version); err != nil {
		_ = provider.Close()
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
}

func h00C35Latest(t *testing.T) int64 {
	t.Helper()
	latest, err := migrations.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	return latest
}

func h00C35CaptureContains(t *testing.T, capture *h00C35Capture, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if strings.Contains(capture.String(), want) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("did not observe %q in command output", want)
		case <-ticker.C:
		}
	}
}

type h00C35Capture struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *h00C35Capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}

func (c *h00C35Capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

func h00C35UnusedAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func h00C35RunAPIUntilReadyToStop(t *testing.T, path string) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr h00C35Capture
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- runAPI(ctx, nil, &stdout, &stderr) }()
	h00C35CaptureContains(t, &stderr, "api listening")
	cancel()
	select {
	case code := <-result:
		return code, stdout.String(), stderr.String()
	case <-time.After(5 * time.Second):
		t.Fatalf("API did not stop after context cancellation (config %s)", path)
		return 1, "", ""
	}
}

func TestH00C3StartupCrossProductRefusesBeforeListenOrClaim(t *testing.T) {
	requireH00CLIDatabase(t)
	latest := h00C35Latest(t)
	t.Cleanup(func() { h00C35ResetSchema(t, latest) })
	workload := fmt.Sprintf("h00-c3-startup-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)
	// The genuine v3 fixture is created by migrating a clean disposable schema
	// only through migration 00003, not by editing the goose version table.
	t.Setenv("TEST_DATABASE_RUNTIME_URL", os.Getenv("TEST_DATABASE_URL"))

	cases := []struct {
		name    string
		prepare func()
		want    string
	}{
		{name: "old-v3-schema", prepare: func() { h00C35ResetSchema(t, 3) }, want: "startup schema fence"},
		{name: "configured-missing", prepare: func() { h00C35ResetSchema(t, latest) }, want: "startup policy fence"},
		{name: "null-topology-digest", prepare: func() {
			h00C35ResetSchema(t, latest)
			code, _, stderr := runH00CLI(t, "policy", "apply", configPath)
			if code != exitOk {
				t.Fatalf("seed policy apply = %d %s", code, stderr)
			}
			if _, err := h00C35AdminDB(t).ExecContext(context.Background(),
				`UPDATE workload_policies SET topology_digest = NULL WHERE name = $1`, workload); err != nil {
				t.Fatal(err)
			}
		}, want: "startup policy fence"},
		{name: "mismatched-digest", prepare: func() {
			h00C35ResetSchema(t, latest)
			code, _, stderr := runH00CLI(t, "policy", "apply", configPath)
			if code != exitOk {
				t.Fatalf("seed policy apply = %d %s", code, stderr)
			}
			if _, err := h00C35AdminDB(t).ExecContext(context.Background(),
				`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, make([]byte, 32), workload); err != nil {
				t.Fatal(err)
			}
		}, want: "startup policy fence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			before := h00C35CaptureSnapshot(t)
			var apiOut, apiErr bytes.Buffer
			apiCode := runAPI(context.Background(), nil, &apiOut, &apiErr)
			if apiCode != 1 || apiOut.Len() != 0 || !strings.Contains(apiErr.String(), tc.want) || strings.Contains(apiErr.String(), "api listening") {
				t.Fatalf("API startup = %d stdout=%q stderr=%q", apiCode, apiOut.String(), apiErr.String())
			}
			var workerOut, workerErr bytes.Buffer
			workerCode := runWorkerMain(context.Background(), &workerOut, &workerErr)
			if workerCode != 1 || workerOut.Len() != 0 || !strings.Contains(workerErr.String(), tc.want) || strings.Contains(workerErr.String(), "worker starting") {
				t.Fatalf("worker startup = %d stdout=%q stderr=%q", workerCode, workerOut.String(), workerErr.String())
			}
			after := h00C35CaptureSnapshot(t)
			if after != before {
				t.Fatalf("startup fence mutated durable state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestH00C3StartupContinuesForMutableDriftWithBoundedUnmanagedWarning(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-c3-continue-%d", time.Now().UnixNano())
	unmanaged := fmt.Sprintf("h00-c3-retained-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)
	t.Setenv("TEST_DATABASE_RUNTIME_URL", os.Getenv("TEST_DATABASE_URL"))
	code, _, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk {
		t.Fatalf("seed apply = %d %s", code, stderr)
	}
	db := h00C35AdminDB(t)
	var id int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM workload_policies WHERE name=$1`, workload).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workload_policies SET max_active_tasks=max_active_tasks+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := testsupport.ApplyPolicy(context.Background(), mustAdminStore(t), postgres.WorkloadPolicy{
		Name: unmanaged, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128, AllowedGeneration: []byte(`{}`),
		RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workload_policies WHERE name=$1`, unmanaged)
	})

	before := h00C35CaptureSnapshot(t)
	addr := h00C35UnusedAddr(t)
	apiConfig := strings.Replace(h00CLIConfig(t, workload), `127.0.0.1:8010`, addr, 1)
	apiPath := writeTempConfig(t, apiConfig)
	t.Setenv(configEnvVar, apiPath)
	apiCode, _, apiOutput := h00C35RunAPIUntilReadyToStop(t, apiPath)
	if apiCode != exitOk || strings.Count(apiOutput, "unmanaged durable workloads retained") != 1 {
		t.Fatalf("API continued startup = %d output=%q", apiCode, apiOutput)
	}

	var workerOut h00C35Capture
	ctx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan int, 1)
	go func() { workerDone <- runWorkerMain(ctx, io.Discard, &workerOut) }()
	h00C35CaptureContains(t, &workerOut, "unmanaged durable workloads retained")
	h00C35CaptureContains(t, &workerOut, "worker active")
	cancel()
	select {
	case workerCode := <-workerDone:
		if workerCode != exitOk {
			t.Fatalf("worker continued startup exit = %d output=%q", workerCode, workerOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after continued startup")
	}
	if strings.Count(workerOut.String(), "unmanaged durable workloads retained") != 1 {
		t.Fatalf("worker unmanaged warning was not bounded: %q", workerOut.String())
	}
	if after := h00C35CaptureSnapshot(t); after.policies != before.policies || after.grants != before.grants || after.tasks != before.tasks || after.attempts != before.attempts || after.results != before.results {
		t.Fatalf("continued startup mutated durable workload state: before=%+v after=%+v", before, after)
	}
}

func mustAdminStore(t *testing.T) *postgres.Store {
	t.Helper()
	store, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_URL"), postgres.Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func h00C35PolicyConfig(workloads ...string) string {
	entries := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		entries = append(entries, fmt.Sprintf(`%q: {
      "priority_class": "normal",
      "max_active_tasks": 10,
      "max_attempts": 3,
      "retry_delays_seconds": [1, 5],
      "max_output_tokens": 2048,
      "allowed_generation": {},
      "required_features": [],
      "allowed_capabilities": ["instruct"],
      "workload_timeout_seconds": 60
    }`, workload))
	}
	return fmt.Sprintf(`{
  "version": 2,
  "api": {"listen": "127.0.0.1:8010"},
  "database": {
    "url_env": "TEST_DATABASE_RUNTIME_URL",
    "provisioner_url_env": "TEST_DATABASE_PROVISIONER_URL",
    "migration_url_env": "TEST_DATABASE_URL",
    "max_connections": 4,
    "connect_timeout_seconds": 5
  },
  "retention": {"payload": "1h", "result": "168h", "metadata": "720h"},
  "backends": {"fake": {"type": "fake"}},
  "capabilities": {"instruct": {"backend": "fake"}},
  "workloads": {%s}
}`, strings.Join(entries, ",\n"))
}

func TestH00C5PolicyValidateIsIndependentOfDatabaseEnvironment(t *testing.T) {
	valid := writeTempConfig(t, h00C35PolicyConfig("h00-c5-valid"))
	invalid := writeTempConfig(t, strings.Replace(h00C35PolicyConfig("h00-c5-invalid"),
		`"workload_timeout_seconds": 60`, `"workload_timeout_seconds": 0`, 1))
	t.Setenv(configEnvVar, valid)
	for _, name := range []string{"TEST_DATABASE_URL", "TEST_DATABASE_PROVISIONER_URL", "TEST_DATABASE_RUNTIME_URL"} {
		t.Setenv(name, "definitely-not-a-database-url")
	}
	cases := []struct {
		name string
		args []string
		path string
		code int
	}{
		{name: "explicit-valid", args: []string{"validate", valid}, path: valid, code: exitOk},
		{name: "environment-valid", args: []string{"validate"}, path: valid, code: exitOk},
		{name: "explicit-invalid", args: []string{"validate", invalid}, path: invalid, code: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path != valid {
				t.Setenv(configEnvVar, tc.path)
			} else {
				t.Setenv(configEnvVar, valid)
			}
			var stdout, stderr bytes.Buffer
			code := runPolicy(context.Background(), tc.args, &stdout, &stderr)
			if code != tc.code {
				t.Fatalf("policy validate exit = %d, want %d; stdout=%q stderr=%q", code, tc.code, stdout.String(), stderr.String())
			}
			lowerStderr := strings.ToLower(stderr.String())
			if strings.Contains(lowerStderr, "database connection") ||
				strings.Contains(lowerStderr, "database.provisioner") ||
				strings.Contains(lowerStderr, "environment variable test_database") {
				t.Fatalf("database-oriented validate error: %q", stderr.String())
			}
		})
	}
}

func TestH00C5PolicyStatusExitAndReadOnlyMatrix(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-c5-status-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00C35PolicyConfig(workload))
	t.Setenv(configEnvVar, configPath)
	code, stdout, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" || !strings.Contains(stdout, "create=1") {
		t.Fatalf("seed apply = %d %q %q", code, stdout, stderr)
	}
	db := h00C35AdminDB(t)
	base := h00C35CaptureSnapshot(t)
	for _, sub := range []string{"validate", "diff", "status"} {
		args := []string{"policy", sub, configPath}
		code, _, stderr = runH00CLI(t, args...)
		if code != exitOk || stderr != "" {
			t.Fatalf("coherent %s = %d stderr=%q", sub, code, stderr)
		}
	}
	code, stdout, stderr = runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || stderr != "" || stdout != "apply: create=0 update=0 unchanged=1 unmanaged=0 topology-mismatch=0\n" {
		t.Fatalf("no-op apply = %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if after := h00C35CaptureSnapshot(t); after != base {
		t.Fatalf("read-only/no-op policy commands changed snapshot: before=%+v after=%+v", base, after)
	}

	policyID := int64(0)
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM workload_policies WHERE name=$1`, workload).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workload_policies SET max_active_tasks=max_active_tasks+1 WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != 2 || stdout != "status: create=0 update=1 unchanged=0 unmanaged=0 topology-mismatch=0\n" || stderr != "" {
		t.Fatalf("policy drift status = %d %q %q", code, stdout, stderr)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workload_policies SET max_active_tasks=10 WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workload_policies SET topology_digest=NULL WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != 2 || !strings.Contains(stdout, "topology-mismatch=1") || stderr != "" {
		t.Fatalf("null topology status = %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || !strings.Contains(stdout, "topology-mismatch=1") || stderr == "" {
		t.Fatalf("topology apply = %d %q %q", code, stdout, stderr)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM workload_policies WHERE id=$1`, policyID); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != 2 || !strings.Contains(stdout, "create=1") || stderr != "" {
		t.Fatalf("missing policy status = %d %q %q", code, stdout, stderr)
	}
	// An operational opening failure is exit 1 and is not a drift category.
	oldProvisioner := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	t.Setenv("TEST_DATABASE_PROVISIONER_URL", "not-a-database-url")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	code = runPolicy(ctx, []string{"status", configPath}, io.Discard, &bytes.Buffer{})
	cancel()
	if code != 1 {
		t.Fatalf("operational policy status exit = %d, want 1", code)
	}
	t.Setenv("TEST_DATABASE_PROVISIONER_URL", oldProvisioner)
}

func TestH00C5TimeoutIsLiveButCreateAndTopologyRequireGuidance(t *testing.T) {
	requireH00CLIDatabase(t)
	first := fmt.Sprintf("h00-c5-timeout-%d", time.Now().UnixNano())
	second := first + "-new"
	basePath := writeTempConfig(t, h00C35PolicyConfig(first))
	t.Setenv(configEnvVar, basePath)
	code, stdout, stderr := runH00CLI(t, "policy", "apply", basePath)
	if code != exitOk || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("base apply = %d %q", code, stderr)
	}
	updated := strings.Replace(h00C35PolicyConfig(first), `"workload_timeout_seconds": 60`, `"workload_timeout_seconds": 61`, 1)
	updatedPath := writeTempConfig(t, updated)
	code, stdout, stderr = runH00CLI(t, "policy", "apply", updatedPath)
	if code != exitOk || stdout != "apply: create=0 update=1 unchanged=0 unmanaged=0 topology-mismatch=0\n" || stderr != "" {
		t.Fatalf("timeout apply = %d %q %q", code, stdout, stderr)
	}
	db := h00C35AdminDB(t)
	var timeout int
	if err := db.QueryRowContext(context.Background(), `SELECT workload_timeout_seconds FROM workload_policies WHERE name=$1`, first).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 61 {
		t.Fatalf("live timeout = %d, want 61", timeout)
	}
	newConfig := strings.Replace(h00C35PolicyConfig(first, second), `"workload_timeout_seconds": 60`, `"workload_timeout_seconds": 61`, 1)
	newPath := writeTempConfig(t, newConfig)
	code, stdout, stderr = runH00CLI(t, "policy", "apply", newPath)
	if code != exitOk || stdout != "apply: create=1 update=0 unchanged=1 unmanaged=0 topology-mismatch=0\n" || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("new workload apply = %d %q %q", code, stdout, stderr)
	}
	topology := strings.Replace(newConfig, `"backends": {"fake": {"type": "fake"}}`,
		`"backends": {"fake": {"type": "openai_compatible", "base_url": "http://127.0.0.1:18080", "model": "h00-c5-topology", "credential_env": "H00_C5_TOKEN"}}`, 1)
	topologyPath := writeTempConfig(t, topology)
	code, stdout, stderr = runH00CLI(t, "policy", "apply", topologyPath)
	if code != exitOk || stdout != "apply: create=0 update=0 unchanged=0 unmanaged=0 topology-mismatch=2\n" || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("topology apply = %d %q %q", code, stdout, stderr)
	}
}

func TestH00C5ApplyNeverRenamesOrPrunesAndReportsUnmanaged(t *testing.T) {
	requireH00CLIDatabase(t)
	oldName := fmt.Sprintf("h00-c5-old-%d", time.Now().UnixNano())
	newName := fmt.Sprintf("h00-c5-replacement-%d", time.Now().UnixNano())
	oldPath := writeTempConfig(t, h00C35PolicyConfig(oldName))
	t.Setenv(configEnvVar, oldPath)
	code, stdout, stderr := runH00CLI(t, "policy", "apply", oldPath)
	if code != exitOk || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("old apply = %d %q", code, stderr)
	}
	db := h00C35AdminDB(t)
	oldBefore := h00C35JSON(t, db, `
		SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
		FROM (SELECT id::text AS order_key, p.* FROM workload_policies p WHERE p.name=$1) x`, oldName)
	before := h00C35CaptureSnapshot(t)
	newPath := writeTempConfig(t, h00C35PolicyConfig(newName))
	code, stdout, stderr = runH00CLI(t, "policy", "apply", newPath)
	if code != exitOk || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" || !strings.Contains(stdout, "create=1") {
		t.Fatalf("replacement apply = %d %q %q", code, stdout, stderr)
	}
	afterApply := h00C35CaptureSnapshot(t)
	if before.policies == afterApply.policies {
		t.Fatal("replacement apply did not add the new policy")
	}
	var oldCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM workload_policies WHERE name=$1`, oldName).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 1 {
		t.Fatalf("old durable policy count = %d, want 1", oldCount)
	}
	oldAfter := h00C35JSON(t, db, `
		SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
		FROM (SELECT id::text AS order_key, p.* FROM workload_policies p WHERE p.name=$1) x`, oldName)
	if oldAfter != oldBefore {
		t.Fatalf("replacement changed old durable policy: before=%s after=%s", oldBefore, oldAfter)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", newPath)
	if code != exitOk || !strings.Contains(stdout, "unmanaged=1") || stderr != "" {
		t.Fatalf("replacement status = %d %q %q", code, stdout, stderr)
	}
}

func TestH00C5CLILifecyclePreservesQueuedWorkAndIdentity(t *testing.T) {
	requireH00CLIDatabase(t)
	first := fmt.Sprintf("h00-c5-life-first-%d", time.Now().UnixNano())
	second := fmt.Sprintf("h00-c5-life-second-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00C35PolicyConfig(first, second))
	t.Setenv(configEnvVar, configPath)
	code, stdout, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || stderr == "" {
		t.Fatalf("lifecycle policy apply = %d %q", code, stderr)
	}

	// Every lifecycle mutation is provisioner-only, including credential
	// rotation. Check the policy before performing the successful lifecycle.
	provisionerEnv := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	t.Setenv("TEST_DATABASE_PROVISIONER_URL", "")
	for _, args := range [][]string{
		{"client", "grant", "missing-client", first},
		{"client", "ungrant", "missing-client", first},
		{"client", "disable", "missing-client"},
	} {
		code, stdout, stderr := runH00CLI(t, args...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "TEST_DATABASE_PROVISIONER_URL") {
			t.Fatalf("credentialless %v = %d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	missingCreateCredential := filepath.Join(t.TempDir(), "create-should-not-be-created")
	code, stdout, stderr = runH00CLI(t, "client", "create", "missing-client", "--credential-file", missingCreateCredential)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "TEST_DATABASE_PROVISIONER_URL") {
		t.Fatalf("credentialless create = %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(missingCreateCredential); !os.IsNotExist(err) {
		t.Fatalf("credentialless create touched credential file: %v", err)
	}
	missingCredential := filepath.Join(t.TempDir(), "should-not-be-created")
	code, stdout, stderr = runH00CLI(t, "client", "revoke", "missing-client", "--credential-file", missingCredential)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "TEST_DATABASE_PROVISIONER_URL") {
		t.Fatalf("credentialless revoke = %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(missingCredential); !os.IsNotExist(err) {
		t.Fatalf("credentialless revoke touched credential file: %v", err)
	}
	t.Setenv("TEST_DATABASE_PROVISIONER_URL", provisionerEnv)

	clientName := fmt.Sprintf("h00-c5-life-client-%d", time.Now().UnixNano())
	oldPath := filepath.Join(t.TempDir(), "old.credential")
	code, stdout, stderr = runH00CLI(t, "client", "create", clientName, "--credential-file", oldPath)
	if code != exitOk || stderr != "" {
		t.Fatalf("client create = %d %q", code, stderr)
	}
	oldToken := h00CLIReadCredential(t, oldPath)
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)
	for _, workload := range []string{first, second} {
		code, stdout, stderr = runH00CLI(t, "client", "grant", clientName, workload)
		if code != exitOk || stderr != "" {
			t.Fatalf("grant %s = %d %q", workload, code, stderr)
		}
		assertH00CLINoRawToken(t, oldToken, stdout, stderr)
	}

	provisioner := mustAdminStore(t)
	client, err := provisioner.GetClientByName(context.Background(), clientName)
	if err != nil {
		t.Fatal(err)
	}
	firstPolicy, err := provisioner.GetWorkloadPolicyByName(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := provisioner.GetWorkloadPolicyByName(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_RUNTIME_URL"), postgres.Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeStore.Close)
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := runtimeStore.GetClientByIdentifier(ctx, identifier)
		if err != nil {
			if errors.Is(err, postgres.ErrNotFound) {
				return auth.Identity{}, nil, false, nil
			}
			return auth.Identity{}, nil, false, err
		}
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})
	server := brokerapi.New(brokerapi.Options{Store: runtimeStore, Config: cfg, Verifier: verifier})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	status, body := h00C35HTTP(t, ts, http.MethodPost, "/v1/tasks", oldToken, "h00-c5-life-key-0001", fmt.Sprintf(`{"workload":%q,"capability":"instruct","messages":[{"role":"user","content":"queued"}],"response":{"kind":"text"}}`, first))
	if status != http.StatusCreated {
		t.Fatalf("queued lifecycle task = %d %s", status, body)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = runH00CLI(t, "client", "ungrant", clientName, first)
	if code != exitOk || stderr != "" {
		t.Fatalf("CLI ungrant = %d %q", code, stderr)
	}
	grantedFirst, err := provisioner.ClientHasWorkload(context.Background(), client.ID, firstPolicy.ID)
	if err != nil || grantedFirst {
		t.Fatalf("first grant after ungrant = %t, %v", grantedFirst, err)
	}
	grantedSecond, err := provisioner.ClientHasWorkload(context.Background(), client.ID, secondPolicy.ID)
	if err != nil || !grantedSecond {
		t.Fatalf("second grant after ungrant = %t, %v", grantedSecond, err)
	}
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)

	newPath := filepath.Join(t.TempDir(), "new.credential")
	code, stdout, stderr = runH00CLI(t, "client", "revoke", clientName, "--credential-file", newPath)
	if code != exitOk || stderr != "" {
		t.Fatalf("CLI revoke = %d %q", code, stderr)
	}
	newToken := h00CLIReadCredential(t, newPath)
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)
	assertH00CLINoRawToken(t, newToken, stdout, stderr)
	rotated, err := provisioner.GetClientByName(context.Background(), clientName)
	if err != nil || rotated.ID != client.ID {
		t.Fatalf("client identity changed on rotation: %+v, %v", rotated, err)
	}

	status, _ = h00C35HTTP(t, ts, http.MethodGet, "/v1/tasks/"+task.ID, oldToken, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("old token task read = %d, want 401", status)
	}
	status, _ = h00C35HTTP(t, ts, http.MethodPost, "/v1/tasks", oldToken, "h00-c5-life-old-submit", fmt.Sprintf(`{"workload":%q,"capability":"instruct","messages":[{"role":"user","content":"old"}],"response":{"kind":"text"}}`, second))
	if status != http.StatusUnauthorized {
		t.Fatalf("old token new submission = %d, want 401", status)
	}
	status, body = h00C35HTTP(t, ts, http.MethodGet, "/v1/tasks/"+task.ID, newToken, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"queued"`) {
		t.Fatalf("new token existing task read = %d %s", status, body)
	}
	status, body = h00C35HTTP(t, ts, http.MethodPost, "/v1/tasks", newToken, "h00-c5-life-new-submit", fmt.Sprintf(`{"workload":%q,"capability":"instruct","messages":[{"role":"user","content":"new"}],"response":{"kind":"text"}}`, second))
	if status != http.StatusCreated {
		t.Fatalf("new token new submission = %d %s", status, body)
	}
	_ = stdout
}

func h00C35HTTP(t *testing.T, ts *httptest.Server, method, path, token, key, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, bodyBytes
}
