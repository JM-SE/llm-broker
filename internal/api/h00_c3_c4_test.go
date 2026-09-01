package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

// h00C34Snapshot is deliberately limited to the durable rows that an API
// request can reserve or mutate. JSONB gives the comparison all timestamps,
// hashes, states, and nullable columns without maintaining a second model of
// the schema in the test.
type h00C34Snapshot struct {
	policy   string
	grants   string
	tasks    string
	payloads string
	attempts string
	results  string
}

func h00C34SnapshotFor(t *testing.T, clientID, workloadID int64) h00C34Snapshot {
	t.Helper()
	return h00C34Snapshot{
		policy: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT id::text AS order_key, p.* FROM workload_policies p WHERE p.id = $1) x`, workloadID),
		grants: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT client_id::text || ':' || workload_id::text AS order_key, g.*
			      FROM client_workload_grants g
			      WHERE g.client_id = $1 AND g.workload_id = $2) x`, clientID, workloadID),
		tasks: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT id::text AS order_key, t.* FROM tasks t
			      WHERE t.client_id = $1 AND t.workload_id = $2) x`, clientID, workloadID),
		payloads: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT p.task_id::text AS order_key, p.* FROM task_payloads p
			      JOIN tasks t ON t.id = p.task_id
			      WHERE t.client_id = $1 AND t.workload_id = $2) x`, clientID, workloadID),
		attempts: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT a.id::text AS order_key, a.* FROM task_attempts a
			      JOIN tasks t ON t.id = a.task_id
			      WHERE t.client_id = $1 AND t.workload_id = $2) x`, clientID, workloadID),
		results: h00C34JSON(t, `
			SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
			FROM (SELECT r.task_id::text AS order_key, r.* FROM task_results r
			      JOIN tasks t ON t.id = r.task_id
			      WHERE t.client_id = $1 AND t.workload_id = $2) x`, clientID, workloadID),
	}
}

func h00C34TaskSnapshot(t *testing.T, clientID, workloadID int64) string {
	t.Helper()
	return h00C34JSON(t, `
		SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.order_key), '[]'::jsonb)::text
		FROM (SELECT id::text AS order_key, t.* FROM tasks t
		      WHERE t.client_id = $1 AND t.workload_id = $2) x`, clientID, workloadID)
}

func h00C34JSON(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value string
	if err := adminDB.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("read API no-mutation snapshot: %v", err)
	}
	return value
}

func TestH00C3RuntimeReadinessTracksDurableTopologyOnly(t *testing.T) {
	_, clientID := newClient(t, "h00-c3-ready")
	workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	policy, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatal(err)
	}
	digest := testConfig().TopologyDigest()
	workerID := uuidForH00C3(t)
	if err := adminStore.UpsertWorkerHeartbeat(context.Background(), workerID, "active"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminStore.PruneStaleWorkers(context.Background(), 0)
	})

	cfg := testConfig()
	cfg.API.MetricsEnabled = true
	env := newTestEnvWithConfig(t, cfg)

	cases := []struct {
		name    string
		mutate  func(*testing.T)
		restore func(*testing.T)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T) {
				if _, err := adminDB.ExecContext(context.Background(),
					`DELETE FROM workload_policies WHERE id = $1`, policy.ID); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				_, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
					Name: workload, PriorityClass: policy.PriorityClass, MaxActiveTasks: policy.MaxActiveTasks,
					MaxAttempts: policy.MaxAttempts, RetryDelaysSeconds: policy.RetryDelaysSeconds,
					MaxOutputTokens: policy.MaxOutputTokens, AllowedGeneration: policy.AllowedGeneration,
					RequiredFeatures: policy.RequiredFeatures, AllowedCapabilities: policy.AllowedCapabilities,
					WorkloadTimeoutSeconds:   policy.WorkloadTimeoutSeconds,
					PayloadRetentionSeconds:  policy.PayloadRetentionSeconds,
					ResultRetentionSeconds:   policy.ResultRetentionSeconds,
					MetadataRetentionSeconds: policy.MetadataRetentionSeconds, TopologyDigest: digest[:],
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, workload); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "null-digest",
			mutate: func(t *testing.T) {
				if _, err := adminDB.ExecContext(context.Background(),
					`UPDATE workload_policies SET topology_digest = NULL WHERE id = $1`, mustWorkloadID(t, workload)); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if _, err := adminDB.ExecContext(context.Background(),
					`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, digest[:], workload); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mismatched-digest",
			mutate: func(t *testing.T) {
				wrong := append([]byte(nil), digest[:]...)
				wrong[0]++
				if _, err := adminDB.ExecContext(context.Background(),
					`UPDATE workload_policies SET topology_digest = $1 WHERE id = $2`, wrong, mustWorkloadID(t, workload)); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				if _, err := adminDB.ExecContext(context.Background(),
					`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, digest[:], workload); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mutate(t)
			t.Cleanup(func() { tc.restore(t) })
			before := h00C34TaskSnapshot(t, clientID, policy.ID)
			resp, body := doReq(t, env.ts, http.MethodGet, "/ready", "", "", "", "")
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("ready status = %d, want 503; body %s", resp.StatusCode, body)
			}
			metricsResp, metricsBody := doReq(t, env.ts, http.MethodGet, "/metrics", "", "", "", "")
			if metricsResp.StatusCode != http.StatusOK || !strings.Contains(string(metricsBody), "llm_broker_ready 0") {
				t.Fatalf("readiness metric = %d %s, want ready 0", metricsResp.StatusCode, metricsBody)
			}
			if after := h00C34TaskSnapshot(t, clientID, policy.ID); after != before {
				t.Fatalf("readiness mutated task state: before=%s after=%s", before, after)
			}
			tc.restore(t)
			resp, body = doReq(t, env.ts, http.MethodGet, "/ready", "", "", "", "")
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ready":true`) {
				t.Fatalf("coherent readiness = %d %s", resp.StatusCode, body)
			}
		})
	}

}

func TestH00C3MutableDriftAndUnmanagedRowsRemainReady(t *testing.T) {
	_, clientID := newClient(t, "h00-c3-drift")
	workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	policy, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatal(err)
	}
	unmanaged := uniqueName("h00-c3-unmanaged")
	if _, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name: unmanaged, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128, AllowedGeneration: []byte(`{}`),
		RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.ExecContext(context.Background(),
		`UPDATE workload_policies SET max_active_tasks = max_active_tasks + 1 WHERE id = $1`, policy.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), `DELETE FROM workload_policies WHERE name = $1`, unmanaged)
		_, _ = adminDB.ExecContext(context.Background(),
			`UPDATE workload_policies SET max_active_tasks = $1 WHERE id = $2`, policy.MaxActiveTasks, policy.ID)
		delete(testWorkloads, workload)
	})

	workerID := uuidForH00C3(t)
	if err := adminStore.UpsertWorkerHeartbeat(context.Background(), workerID, "active"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminStore.PruneStaleWorkers(context.Background(), 0) })
	cfg := testConfig()
	env := newTestEnvWithConfig(t, cfg)
	resp, body := doReq(t, env.ts, http.MethodGet, "/ready", "", "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ready":true`) {
		t.Fatalf("mutable/unmanaged readiness = %d %s", resp.StatusCode, body)
	}
}

// uuidForH00C3 keeps the readiness fixtures independent of the worker package.
func uuidForH00C3(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.New()
}

func TestH00C4CredentialRecheckRejectsStaleAuthenticatedIdentity(t *testing.T) {
	oldToken, clientID := newClient(t, "h00-c4-stale")
	workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	lookupReturned := make(chan struct{})
	releaseLookup := make(chan struct{})
	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := rtStore.GetClientByIdentifier(ctx, identifier)
		if err != nil {
			return auth.Identity{}, nil, false, err
		}
		close(lookupReturned)
		<-releaseLookup
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})
	srv := New(Options{Store: rtStore, Config: testConfig(), Verifier: verifier, Logger: discardLogger()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	requestDone := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/tasks", strings.NewReader(submission(workload)))
		if err != nil {
			requestErr <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+oldToken.Value)
		req.Header.Set("Idempotency-Key", uniqueName("h00-c4-stale-key"))
		resp, err := ts.Client().Do(req)
		if err != nil {
			requestErr <- err
			return
		}
		requestDone <- resp
	}()

	select {
	case <-lookupReturned:
	case err := <-requestErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("authentication lookup did not reach stale-identity barrier")
	}
	newToken, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminStore.RotateClientCredential(context.Background(), clientID, newToken.Identifier, newToken.Digest[:]); err != nil {
		t.Fatal(err)
	}
	close(releaseLookup)
	select {
	case err := <-requestErr:
		t.Fatal(err)
	case resp := <-requestDone:
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("stale authenticated submission status = %d, want 401", resp.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale authenticated submission did not finish")
	}
	if got := h00C34TaskSnapshot(t, clientID, mustWorkloadID(t, workload)); got != "[]" {
		t.Fatalf("stale credential created durable task: %s", got)
	}
}

func mustWorkloadID(t *testing.T, name string) int64 {
	t.Helper()
	p, err := adminStore.GetWorkloadPolicyByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestH00C4DeniedIdempotencyAndAdmissionCasesAreAuthorizationFirst(t *testing.T) {
	for _, denial := range []string{"grant", "allowlist"} {
		for _, keyCase := range []string{"same-hash", "conflicting-hash", "new-key-admission-limited"} {
			t.Run(denial+"/"+keyCase, func(t *testing.T) {
				tok, clientID := newClient(t, "h00-c4-denial")
				workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
				policy, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
				if err != nil {
					t.Fatal(err)
				}
				env := newTestEnv(t)
				key := uniqueName("h00-c4-existing-key")
				body := submission(workload)
				resp, responseBody := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", body)
				if resp.StatusCode != http.StatusCreated {
					t.Fatalf("seed task status = %d, body %s", resp.StatusCode, responseBody)
				}
				if denial == "grant" {
					if err := adminStore.RevokeWorkloadBySlug(context.Background(), clientID, workload); err != nil {
						t.Fatal(err)
					}
				} else {
					policy.AllowedCapabilities = []string{}
					if _, err := testsupport.ApplyPolicy(context.Background(), adminStore, policy); err != nil {
						t.Fatal(err)
					}
				}

				requestKey := key
				requestBody := body
				switch keyCase {
				case "conflicting-hash":
					requestBody = strings.Replace(body, "hi", "different", 1)
				case "new-key-admission-limited":
					requestKey = uniqueName("h00-c4-admission-key")
				}
				before := h00C34SnapshotFor(t, clientID, policy.ID)
				resp, responseBody = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, requestKey, "", requestBody)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("denied %s/%s status = %d, want 403; body %s", denial, keyCase, resp.StatusCode, responseBody)
				}
				if after := h00C34SnapshotFor(t, clientID, policy.ID); after != before {
					t.Fatalf("denied %s/%s mutated durable rows: before=%+v after=%+v", denial, keyCase, before, after)
				}
			})
		}
	}
}

func TestH00C4AuthenticationTopologyAndParsingDenialsAreZeroDelta(t *testing.T) {
	tok, clientID := newClient(t, "h00-c4-zero-delta")
	workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	policy, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t)
	unknown, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		token   string
		body    string
		want    int
		mutate  func(*testing.T)
		restore func(*testing.T)
	}{
		{name: "missing-auth", want: http.StatusUnauthorized},
		{name: "unknown-auth", token: unknown.Value, want: http.StatusUnauthorized},
		{name: "malformed-body", token: tok.Value, body: `{"workload":`, want: http.StatusBadRequest},
		{
			name: "topology-mismatch", token: tok.Value, body: submission(workload), want: http.StatusServiceUnavailable,
			mutate: func(t *testing.T) {
				wrong := make([]byte, 32)
				wrong[0] = 0x7f
				if _, err := adminDB.ExecContext(context.Background(), `UPDATE workload_policies SET topology_digest=$1 WHERE id=$2`, wrong, policy.ID); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t *testing.T) {
				digest := testConfig().TopologyDigest()
				if _, err := adminDB.ExecContext(context.Background(), `UPDATE workload_policies SET topology_digest=$1 WHERE id=$2`, digest[:], policy.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate != nil {
				tc.mutate(t)
				t.Cleanup(func() { tc.restore(t) })
			}
			if tc.body == "" && tc.name != "missing-auth" && tc.name != "unknown-auth" {
				tc.body = submission(workload)
			}
			before := h00C34SnapshotFor(t, clientID, policy.ID)
			resp, responseBody := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tc.token, uniqueName("h00-c4-zero-key"), "", tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, tc.want, responseBody)
			}
			if after := h00C34SnapshotFor(t, clientID, policy.ID); after != before {
				t.Fatalf("%s changed durable state: before=%+v after=%+v", tc.name, before, after)
			}
		})
	}
}

func TestH00C4CredentialCanaryDoesNotCrossVerifierBoundary(t *testing.T) {
	tok, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	var seenIdentifier string
	var seenDigest []byte
	verifier := auth.NewVerifier(func(_ context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		seenIdentifier = identifier
		seenDigest = append([]byte(nil), tok.Digest[:]...)
		return auth.Identity{ClientID: 42, Name: "canary-client"}, seenDigest, true, nil
	})
	identity, err := verifier.Verify(context.Background(), tok.Value)
	if err != nil {
		t.Fatal(err)
	}
	if seenIdentifier == tok.Value || strings.Contains(seenIdentifier, tok.Value) {
		t.Fatalf("raw credential crossed lookup identifier boundary: %q", seenIdentifier)
	}
	if !bytes.Equal(seenDigest, tok.Digest[:]) || identity.DigestProof != tok.Digest {
		t.Fatalf("digest proof boundary mismatch: lookup=%x identity=%x", seenDigest, identity.DigestProof)
	}
	for _, value := range []string{fmt.Sprint(identity), fmt.Sprint(err)} {
		if strings.Contains(value, tok.Value) {
			t.Fatalf("raw credential appeared in captured boundary value %q", value)
		}
	}
}
