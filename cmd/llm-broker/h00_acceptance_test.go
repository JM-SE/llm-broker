package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
	"github.com/JM-SE/llm-broker/migrations"
	"github.com/pressly/goose/v3"
)

func requireH00CLIDatabase(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TEST_DATABASE_URL", "TEST_DATABASE_PROVISIONER_URL", "TEST_DATABASE_RUNTIME_URL",
	} {
		if os.Getenv(name) == "" {
			if os.Getenv("LLM_BROKER_REQUIRE_TEST_DATABASE") == "1" {
				t.Fatalf("H00 CLI acceptance test requires TEST_DATABASE_URL, TEST_DATABASE_PROVISIONER_URL, and TEST_DATABASE_RUNTIME_URL")
			}
			t.Skipf("H00 CLI acceptance test requires %s", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := postgres.Open(ctx, os.Getenv("TEST_DATABASE_URL"), postgres.Options{
		MaxConns: 4, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open H00 CLI migration store: %v", err)
	}
	defer admin.Close()
	adminDB, err := testsupport.OpenDatabase(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open H00 CLI SQL bridge: %v", err)
	}
	if _, err := adminDB.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		_ = adminDB.Close()
		t.Fatalf("reset H00 CLI database: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, adminDB, migrations.FS)
	if err != nil {
		_ = adminDB.Close()
		t.Fatalf("create H00 CLI migration provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		_ = provider.Close()
		_ = adminDB.Close()
		t.Fatalf("migrate H00 CLI database: %v", err)
	}
	if err := provider.Close(); err != nil {
		_ = adminDB.Close()
		t.Fatalf("close H00 CLI migration provider: %v", err)
	}
	if err := adminDB.Close(); err != nil {
		t.Fatalf("close H00 CLI SQL bridge: %v", err)
	}
}

func h00CLIConfig(t *testing.T, workload string) string {
	t.Helper()
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
  "workloads": {
    %q: {
      "priority_class": "normal",
      "max_active_tasks": 10,
      "max_attempts": 3,
      "retry_delays_seconds": [1, 5],
      "max_output_tokens": 2048,
      "allowed_generation": {},
      "required_features": [],
      "allowed_capabilities": ["instruct"],
      "workload_timeout_seconds": 60
    }
  }
}`, workload)
}

func h00CLIReadCredential(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential file mode = %o, want 600", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file %s: %v", path, err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		t.Fatalf("credential file %s is empty", path)
	}
	return token
}

func assertH00CLINoRawToken(t *testing.T, token string, stdout, stderr string) {
	t.Helper()
	if strings.Contains(stdout, token) || strings.Contains(stderr, token) {
		t.Fatalf("credential was printed; stdout=%q stderr=%q", stdout, stderr)
	}
}

func runH00CLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"llm-broker"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestH00PolicyAndClientCLILifecycle(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-cli-workload-%d", time.Now().UnixNano())
	clientName := fmt.Sprintf("h00-cli-client-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)

	code, stdout, stderr := runH00CLI(t, "policy", "validate", configPath)
	if code != exitOk || stderr != "" || !strings.Contains(stdout, "valid configuration") {
		t.Fatalf("policy validate = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "diff", configPath)
	wantDiff := "diff: create=1 update=0 unchanged=0 unmanaged=0 topology-mismatch=0\n"
	if code != 2 || stdout != wantDiff || stderr != "" {
		t.Fatalf("policy diff = exit %d stdout=%q stderr=%q, want exit 2/%q", code, stdout, stderr, wantDiff)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "apply", configPath)
	wantApply := "apply: create=1 update=0 unchanged=0 unmanaged=0 topology-mismatch=0\n"
	wantApplyWarning := "policy apply: topology changed; planned stop/apply/restart is required before serving\n"
	if code != exitOk || stdout != wantApply || stderr != wantApplyWarning {
		t.Fatalf("policy apply = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantApply)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	wantStatus := "status: create=0 update=0 unchanged=1 unmanaged=0 topology-mismatch=0\n"
	if code != exitOk || stdout != wantStatus || stderr != "" {
		t.Fatalf("policy status = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantStatus)
	}

	credentialPath := filepath.Join(t.TempDir(), "client.credential")
	code, stdout, stderr = runH00CLI(t, "client", "create", clientName,
		"--credential-file", credentialPath)
	if code != exitOk || stderr != "" {
		t.Fatalf("client create = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	oldToken := h00CLIReadCredential(t, credentialPath)
	wantCreate := fmt.Sprintf("client %q created; credential written to %s (mode 0600)\n", clientName, credentialPath)
	if stdout != wantCreate {
		t.Fatalf("client create stdout = %q, want %q", stdout, wantCreate)
	}
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)

	provisioner, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_PROVISIONER_URL"), postgres.Options{
		MaxConns: 4, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open H00 CLI verification store: %v", err)
	}
	defer provisioner.Close()
	client, err := provisioner.GetClientByName(context.Background(), clientName)
	if err != nil {
		t.Fatalf("read CLI-created client: %v", err)
	}
	policy, err := provisioner.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatalf("read CLI-applied workload: %v", err)
	}

	code, stdout, stderr = runH00CLI(t, "client", "grant", clientName, workload)
	wantGrant := fmt.Sprintf("granted workload %q to client %q\n", workload, clientName)
	if code != exitOk || stdout != wantGrant || stderr != "" {
		t.Fatalf("client grant = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantGrant)
	}
	hasGrant, err := provisioner.ClientHasWorkload(context.Background(), client.ID, policy.ID)
	if err != nil || !hasGrant {
		t.Fatalf("grant after CLI grant = %v, %v; want true", hasGrant, err)
	}
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)

	code, stdout, stderr = runH00CLI(t, "client", "ungrant", clientName, workload)
	wantUngrant := fmt.Sprintf("removed workload grant for client %q\n", clientName)
	if code != exitOk || stdout != wantUngrant || stderr != "" {
		t.Fatalf("client ungrant = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantUngrant)
	}
	hasGrant, err = provisioner.ClientHasWorkload(context.Background(), client.ID, policy.ID)
	if err != nil || hasGrant {
		t.Fatalf("grant after CLI ungrant = %v, %v; want false", hasGrant, err)
	}
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)

	rotatedPath := filepath.Join(t.TempDir(), "client.rotated.credential")
	code, stdout, stderr = runH00CLI(t, "client", "revoke", clientName,
		"--credential-file", rotatedPath)
	if code != exitOk || stderr != "" {
		t.Fatalf("client revoke = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	newToken := h00CLIReadCredential(t, rotatedPath)
	if got := h00CLIReadCredential(t, credentialPath); got != oldToken {
		t.Fatal("original credential file changed during revoke")
	}
	wantRevoke := fmt.Sprintf("client %q credential rotated; credential written to %s (mode 0600)\n", clientName, rotatedPath)
	if stdout != wantRevoke {
		t.Fatalf("client revoke stdout = %q, want %q", stdout, wantRevoke)
	}
	assertH00CLINoRawToken(t, oldToken, stdout, stderr)
	assertH00CLINoRawToken(t, newToken, stdout, stderr)

	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := provisioner.GetClientByIdentifier(ctx, identifier)
		if err != nil {
			if errors.Is(err, postgres.ErrNotFound) {
				return auth.Identity{}, nil, false, nil
			}
			return auth.Identity{}, nil, false, err
		}
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})
	if _, err := verifier.Verify(context.Background(), oldToken); err == nil {
		t.Fatal("revoked credential still verifies")
	}
	if _, err := verifier.Verify(context.Background(), newToken); err != nil {
		t.Fatalf("rotated credential does not verify: %v", err)
	}

	code, stdout, stderr = runH00CLI(t, "client", "disable", clientName)
	wantDisable := fmt.Sprintf("client %q disabled; its credential is rejected\n", clientName)
	if code != exitOk || stdout != wantDisable || stderr != "" {
		t.Fatalf("client disable = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantDisable)
	}
	assertH00CLINoRawToken(t, newToken, stdout, stderr)
	if _, err := verifier.Verify(context.Background(), newToken); err == nil {
		t.Fatal("disabled credential still verifies")
	}
}

func TestH00WorkerStartupFailsClosedForUnprovisionedTopology(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-worker-fence-workload-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)

	var stdout, stderr bytes.Buffer
	code := runWorkerMain(context.Background(), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("worker startup exit = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "startup policy fence") {
		t.Fatalf("worker startup error = %q, want policy fence", stderr.String())
	}
}

func TestH00PolicyValidateIsDatabaseFreeAndOtherCommandsRequireCredentials(t *testing.T) {
	workload := "h00-cli-no-credential-workload"
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)
	for _, envName := range []string{
		"TEST_DATABASE_URL", "TEST_DATABASE_PROVISIONER_URL", "TEST_DATABASE_RUNTIME_URL",
	} {
		t.Setenv(envName, "")
	}

	code, stdout, stderr := runH00CLI(t, "policy", "validate", configPath)
	if code != exitOk || !strings.Contains(stdout, "valid configuration") || stderr != "" {
		t.Fatalf("explicit database-free validate = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "validate")
	if code != exitOk || !strings.Contains(stdout, "valid configuration") || stderr != "" {
		t.Fatalf("env-path database-free validate = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	for _, subcommand := range []string{"diff", "apply", "status"} {
		t.Run(subcommand+"-without-credential", func(t *testing.T) {
			code, stdout, stderr := runH00CLI(t, "policy", subcommand, configPath)
			if code != 1 {
				t.Fatalf("%s without credential exit = %d, want 1; stdout=%q stderr=%q", subcommand, code, stdout, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "TEST_DATABASE_PROVISIONER_URL") {
				t.Fatalf("%s without credential output = stdout=%q stderr=%q", subcommand, stdout, stderr)
			}
		})
	}
}

func TestH00PolicyReadOnlyDriftStatusAndApplyExitMatrix(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-cli-drift-workload-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)

	code, stdout, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" || !strings.Contains(stdout, "create=1") {
		t.Fatalf("seed policy apply = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	provisioner, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_PROVISIONER_URL"), postgres.Options{
		MaxConns: 4, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open drift verification store: %v", err)
	}
	defer provisioner.Close()
	actual, err := provisioner.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatalf("read seeded drift policy: %v", err)
	}
	unchangedAt := actual.UpdatedAt
	provisionerDB, err := testsupport.OpenDatabase(os.Getenv("TEST_DATABASE_PROVISIONER_URL"))
	if err != nil {
		t.Fatalf("open drift verification SQL bridge: %v", err)
	}
	defer provisionerDB.Close()
	if _, err := provisionerDB.ExecContext(context.Background(),
		`UPDATE workload_policies SET max_active_tasks = max_active_tasks + 1 WHERE id = $1`, actual.ID); err != nil {
		t.Fatalf("create mutable policy drift: %v", err)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "diff", configPath)
	if code != 2 || !strings.Contains(stdout, "update=1") || stderr != "" {
		t.Fatalf("drift diff = exit %d stdout=%q stderr=%q, want exit 2/update=1", code, stdout, stderr)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != 2 || !strings.Contains(stdout, "update=1") || stderr != "" {
		t.Fatalf("drift status = exit %d stdout=%q stderr=%q, want exit 2/update=1", code, stdout, stderr)
	}
	actual, err = provisioner.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatalf("read policy after read-only commands: %v", err)
	}
	if !actual.UpdatedAt.Equal(unchangedAt) {
		t.Fatalf("read-only drift commands changed updated_at: before=%v after=%v", unchangedAt, actual.UpdatedAt)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || !strings.Contains(stdout, "update=1") || stderr != "" {
		t.Fatalf("drift apply = exit %d stdout=%q stderr=%q, want exit 0/update=1", code, stdout, stderr)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != exitOk || !strings.Contains(stdout, "unchanged=1") || stderr != "" {
		t.Fatalf("coherent status after apply = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	unmanaged := fmt.Sprintf("h00-cli-unmanaged-%d", time.Now().UnixNano())
	if _, err := testsupport.ApplyPolicy(context.Background(), provisioner, postgres.WorkloadPolicy{
		Name:                     unmanaged,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1},
		MaxOutputTokens:          128,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		AllowedCapabilities:      []string{"instruct"},
		WorkloadTimeoutSeconds:   60,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
	}); err != nil {
		t.Fatalf("create unmanaged policy: %v", err)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "diff", configPath)
	if code != exitOk || !strings.Contains(stdout, "unmanaged=1") || stderr != "" {
		t.Fatalf("unmanaged diff = exit %d stdout=%q stderr=%q, want exit 0/unmanaged=1", code, stdout, stderr)
	}
	code, stdout, stderr = runH00CLI(t, "policy", "status", configPath)
	if code != exitOk || !strings.Contains(stdout, "unmanaged=1") || stderr != "" {
		t.Fatalf("unmanaged status = exit %d stdout=%q stderr=%q, want exit 0/unmanaged=1", code, stdout, stderr)
	}
}

func TestH00PolicyApplyReportsTopologyChangeAndRestartRequirement(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-cli-topology-workload-%d", time.Now().UnixNano())
	initial := h00CLIConfig(t, workload)
	configPath := writeTempConfig(t, initial)
	t.Setenv(configEnvVar, configPath)

	code, stdout, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk || stdout == "" || stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("initial topology apply = exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	changed := strings.Replace(initial,
		`"backends": {"fake": {"type": "fake"}}`,
		`"backends": {"fake": {"type": "openai_compatible", "base_url": "http://127.0.0.1:18080", "model": "h00-topology-model", "credential_env": "H00_BACKEND_TOKEN"}}`,
		1,
	)
	if changed == initial {
		t.Fatal("topology-changing configuration replacement did not match")
	}
	if err := os.WriteFile(configPath, []byte(changed), 0o600); err != nil {
		t.Fatalf("rewrite topology configuration: %v", err)
	}

	code, stdout, stderr = runH00CLI(t, "policy", "apply", configPath)
	wantSummary := "apply: create=0 update=0 unchanged=0 unmanaged=0 topology-mismatch=1\n"
	if code != exitOk || stdout != wantSummary {
		t.Fatalf("topology apply = exit %d stdout=%q stderr=%q, want exit 0/%q", code, stdout, stderr, wantSummary)
	}
	if stderr != "policy apply: topology changed; planned stop/apply/restart is required before serving\n" {
		t.Fatalf("topology apply warning = %q", stderr)
	}
}

func TestH00ConcurrentPolicyApplyUsesOneCommittedProjection(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-cli-concurrent-workload-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)

	type result struct {
		code   int
		stdout string
		stderr string
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"llm-broker", "policy", "apply", configPath}, &stdout, &stderr)
			results <- result{code: code, stdout: stdout.String(), stderr: stderr.String()}
		}()
	}
	seenCreate, seenUnchanged := false, false
	for i := 0; i < 2; i++ {
		got := <-results
		wantWarning := strings.Contains(got.stdout, "create=1")
		wantStderr := ""
		if wantWarning {
			wantStderr = "policy apply: topology changed; planned stop/apply/restart is required before serving\n"
		}
		if got.code != exitOk || got.stderr != wantStderr {
			t.Fatalf("concurrent apply result = exit %d stdout=%q stderr=%q", got.code, got.stdout, got.stderr)
		}
		seenCreate = seenCreate || strings.Contains(got.stdout, "create=1")
		seenUnchanged = seenUnchanged || strings.Contains(got.stdout, "unchanged=1")
	}
	if !seenCreate || !seenUnchanged {
		t.Fatalf("concurrent apply reports did not serialize create/no-op: create=%t unchanged=%t", seenCreate, seenUnchanged)
	}

	provisioner, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_PROVISIONER_URL"), postgres.Options{
		MaxConns: 4, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open concurrent-apply verification store: %v", err)
	}
	defer provisioner.Close()
	if _, err := provisioner.GetWorkloadPolicyByName(context.Background(), workload); err != nil {
		t.Fatalf("read concurrently applied policy: %v", err)
	}
}

func TestH00APIStartupFencesSchemaAndConfiguredTopology(t *testing.T) {
	requireH00CLIDatabase(t)
	workload := fmt.Sprintf("h00-api-startup-fence-workload-%d", time.Now().UnixNano())
	configPath := writeTempConfig(t, h00CLIConfig(t, workload))
	t.Setenv(configEnvVar, configPath)
	code, _, stderr := runH00CLI(t, "policy", "apply", configPath)
	if code != exitOk {
		t.Fatalf("seed startup-fence policy apply = %d: %s", code, stderr)
	}

	admin, err := postgres.Open(context.Background(), os.Getenv("TEST_DATABASE_URL"), postgres.Options{
		MaxConns: 2, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open startup-fence verification store: %v", err)
	}
	defer admin.Close()
	ctx := context.Background()
	adminDB, err := testsupport.OpenDatabase(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("open startup-fence SQL bridge: %v", err)
	}
	defer adminDB.Close()
	if _, err := adminDB.ExecContext(ctx,
		"INSERT INTO goose_db_version (version_id, is_applied) VALUES (9998, TRUE)"); err != nil {
		t.Fatalf("seed incompatible schema version: %v", err)
	}
	code, _, stderr = runH00CLI(t, "api")
	if code != 1 || !strings.Contains(stderr, "startup schema fence") {
		t.Fatalf("schema-fenced API startup = exit %d stderr=%q", code, stderr)
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id = 9998"); err != nil {
		t.Fatalf("clear incompatible schema version: %v", err)
	}

	if _, err := adminDB.ExecContext(ctx,
		`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, make([]byte, 32), workload); err != nil {
		t.Fatalf("seed topology drift: %v", err)
	}
	code, _, stderr = runH00CLI(t, "api")
	if code != 1 || !strings.Contains(stderr, "startup policy fence") {
		t.Fatalf("topology-fenced API startup = exit %d stderr=%q", code, stderr)
	}
}
