package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

func h00APIProvisionWorkload(t *testing.T, clientID int64, allowed []string, grant, local bool) string {
	t.Helper()
	name := uniqueName("h00-http-workload")
	digest := testConfig().TopologyDigest()
	_, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1, 5},
		WorkloadTimeoutSeconds:   300,
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte("{}"),
		RequiredFeatures:         []string{},
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      allowed,
		TopologyDigest:           digest[:],
	})
	if err != nil {
		t.Fatalf("provision H00 HTTP workload: %v", err)
	}
	workload, err := adminStore.GetWorkloadPolicyByName(context.Background(), name)
	if err != nil {
		t.Fatalf("read H00 HTTP workload: %v", err)
	}
	if grant {
		if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, workload.Name); err != nil {
			t.Fatalf("grant H00 HTTP workload: %v", err)
		}
	}
	if local {
		testWorkloads[name] = config.WorkloadPolicy{
			PriorityClass:          "normal",
			MaxActiveTasks:         10,
			MaxAttempts:            3,
			RetryDelaysSeconds:     []int{1, 5},
			MaxOutputTokens:        2048,
			AllowedCapabilities:    []string{"instruct"},
			WorkloadTimeoutSeconds: 300,
		}
	}
	return name
}

func h00APISubmission(workload, capability string) string {
	return fmt.Sprintf(`{"workload": %q, "capability": %q,
		"messages": [{"role": "user", "content": "h00 denial"}],
		"response": {"kind": "text"}}`, workload, capability)
}

func assertH00Forbidden(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q, want application/problem+json", got)
	}
	var got problem
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode forbidden problem: %v; body %s", err, body)
	}
	want := problem{
		Type: problemType, Title: "Forbidden", Status: http.StatusForbidden,
		Detail: "workload or capability is not authorized", Code: codeForbidden,
	}
	if got != want {
		t.Fatalf("forbidden problem = %+v, want %+v", got, want)
	}
}

func TestH00SubmissionFailsClosedForAnyConfiguredTopologyMismatch(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "global-topology")
	coherent := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	other := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	digest := testConfig().TopologyDigest()
	t.Cleanup(func() {
		if _, err := adminDB.ExecContext(context.Background(),
			`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, digest[:], other); err != nil {
			t.Errorf("restore unrelated workload topology: %v", err)
		}
		delete(testWorkloads, coherent)
		delete(testWorkloads, other)
	})
	if _, err := adminDB.ExecContext(context.Background(),
		`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, make([]byte, 32), other); err != nil {
		t.Fatalf("make unrelated configured workload incoherent: %v", err)
	}
	resp, body := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value,
		"global-topology-key", "", h00APISubmission(coherent, "instruct"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("global topology mismatch status = %d, want 503; body %s", resp.StatusCode, body)
	}
}

func TestH00TopologyMismatchPrecedesHTTPAuthorizationDenial(t *testing.T) {
	tok, clientID := newClient(t, "topology-precedence")
	coherent := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	localAbsent := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, false)
	missingGrant := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, false, true)
	disallowed := h00APIProvisionWorkload(t, clientID, nil, true, true)
	drifted := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
	for _, name := range []string{coherent, localAbsent, missingGrant, disallowed, drifted} {
		t.Cleanup(func() { delete(testWorkloads, name) })
	}
	env := newTestEnv(t)
	if _, err := adminDB.ExecContext(context.Background(),
		`UPDATE workload_policies SET topology_digest = $1 WHERE name = $2`, make([]byte, 32), drifted); err != nil {
		t.Fatalf("make topology-precedence workload incoherent: %v", err)
	}

	cases := []struct {
		name       string
		workload   string
		capability string
	}{
		{name: "unknown-workload", workload: uniqueName("h00-http-unknown-workload"), capability: "instruct"},
		{name: "unknown-capability", workload: coherent, capability: "unknown-capability"},
		{name: "local-membership", workload: localAbsent, capability: "instruct"},
		{name: "missing-grant", workload: missingGrant, capability: "instruct"},
		{name: "disallowed-capability", workload: disallowed, capability: "instruct"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := uniqueName("h00-http-topology-precedence")
			resp, body := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "",
				h00APISubmission(tc.workload, tc.capability))
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body %s", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("content type = %q, want application/problem+json", got)
			}
			var problemBody problem
			if err := json.Unmarshal(body, &problemBody); err != nil {
				t.Fatalf("decode topology problem: %v; body %s", err, body)
			}
			if problemBody.Code != codeUnavailable || problemBody.Status != http.StatusServiceUnavailable {
				t.Fatalf("problem = %+v, want unavailable 503", problemBody)
			}
			assertH00NoTaskReservation(t, clientID, key)
		})
	}
}

func assertH00NoTaskReservation(t *testing.T, clientID int64, key string) {
	t.Helper()
	var count int
	if err := adminDB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM tasks WHERE client_id=$1 AND idempotency_key=$2`,
		clientID, key).Scan(&count); err != nil {
		t.Fatalf("count denied H00 HTTP task rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("denied request reserved %d task rows for idempotency key %q", count, key)
	}
}

func TestH00HTTPDenialMatrixIsUniformAndNonReserving(t *testing.T) {
	cases := []struct {
		name string
		body func(t *testing.T, clientID int64) string
	}{
		{
			name: "unknown-workload",
			body: func(t *testing.T, clientID int64) string {
				return h00APISubmission(uniqueName("h00-http-unknown-workload"), "instruct")
			},
		},
		{
			name: "unknown-capability",
			body: func(t *testing.T, clientID int64) string {
				workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
				return h00APISubmission(workload, "unknown-capability")
			},
		},
		{
			name: "disallowed-capability",
			body: func(t *testing.T, clientID int64) string {
				workload := h00APIProvisionWorkload(t, clientID, nil, true, true)
				return h00APISubmission(workload, "instruct")
			},
		},
		{
			name: "missing-grant",
			body: func(t *testing.T, clientID int64) string {
				workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, false, true)
				return h00APISubmission(workload, "instruct")
			},
		},
		{
			name: "local-membership-denial",
			body: func(t *testing.T, clientID int64) string {
				workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, false)
				return h00APISubmission(workload, "instruct")
			},
		},
		{
			name: "revoked-authorization",
			body: func(t *testing.T, clientID int64) string {
				workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)
				p, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
				if err != nil {
					t.Fatalf("read H00 revoked workload: %v", err)
				}
				if err := adminStore.RevokeWorkloadBySlug(context.Background(), clientID, p.Name); err != nil {
					t.Fatalf("revoke H00 HTTP workload: %v", err)
				}
				return h00APISubmission(workload, "instruct")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, clientID := newClient(t, "h00-http-denial")
			configured := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, false, true)
			t.Cleanup(func() { delete(testWorkloads, configured) })
			env := newTestEnv(t)
			key := uniqueName("h00-http-idempotency")
			body := tc.body(t, clientID)
			resp, responseBody := doReq(t, env.ts, http.MethodPost, "/v1/tasks",
				tok.Value, key, "", body)
			assertH00Forbidden(t, resp, responseBody)
			assertH00NoTaskReservation(t, clientID, key)
		})
	}
}

func TestH00CredentialRotationRejectsOldTokenButPreservesQueuedTask(t *testing.T) {
	env := newTestEnv(t)
	oldToken, clientID := newClient(t, "h00-http-rotation")
	workload := h00APIProvisionWorkload(t, clientID, []string{"instruct"}, true, true)

	createResp, createBody := doReq(t, env.ts, http.MethodPost, "/v1/tasks",
		oldToken.Value, uniqueName("h00-http-rotation-key"), "", h00APISubmission(workload, "instruct"))
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("queued task creation status = %d; body %s", createResp.StatusCode, createBody)
	}
	id := taskID(t, createBody)

	newToken, err := auth.Generate()
	if err != nil {
		t.Fatalf("generate rotated token: %v", err)
	}
	if _, err := adminStore.RotateClientCredential(context.Background(), clientID,
		newToken.Identifier, newToken.Digest[:]); err != nil {
		t.Fatalf("rotate credential: %v", err)
	}

	resp, body := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+id,
		oldToken.Value, "", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old-token task read status = %d, want 401; body %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, env.ts, http.MethodPost, "/v1/tasks",
		oldToken.Value, uniqueName("h00-http-rotation-old-submit"), "", h00APISubmission(workload, "instruct"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old-token future submission status = %d, want 401; body %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+id,
		newToken.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"queued"`) {
		t.Fatalf("new-token admitted task read = %d %s, want queued task", resp.StatusCode, body)
	}

	stored, err := rtStore.GetTask(context.Background(), uuid.MustParse(id))
	if err != nil {
		t.Fatalf("read rotated task state: %v", err)
	}
	if stored.State != domain.TaskQueued {
		t.Fatalf("rotated task state = %s, want queued", stored.State)
	}
	workloadPolicy, err := adminStore.GetWorkloadPolicyByName(context.Background(), workload)
	if err != nil {
		t.Fatalf("read rotated workload: %v", err)
	}
	granted, err := adminStore.ClientHasWorkload(context.Background(), clientID, workloadPolicy.ID)
	if err != nil {
		t.Fatalf("read grant after rotation: %v", err)
	}
	if !granted {
		t.Fatal("credential rotation removed the workload grant")
	}
}

func TestH00AuthenticationPrecedesMalformedBodyAndReservesNothing(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "h00-http-auth-order")
	unknown, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		token       string
		status      int
		problemCode string
	}{
		{name: "missing-authentication", token: "", status: http.StatusUnauthorized, problemCode: codeUnauthorized},
		{name: "invalid-authentication", token: unknown.Value, status: http.StatusUnauthorized, problemCode: codeUnauthorized},
		{name: "authenticated-malformed-body", token: tok.Value, status: http.StatusBadRequest, problemCode: codeMalformedRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := uniqueName("h00-http-malformed-key")
			resp, body := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tc.token, key, "", `{"workload":`)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, tc.status, body)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("content type = %q, want application/problem+json", got)
			}
			var p problem
			if err := json.Unmarshal(body, &p); err != nil {
				t.Fatalf("decode malformed/auth problem: %v", err)
			}
			if p.Code != tc.problemCode {
				t.Fatalf("problem code = %q, want %q", p.Code, tc.problemCode)
			}
			if strings.Contains(string(body), "h00-http") {
				t.Fatalf("problem body echoed request data: %s", body)
			}
			assertH00NoTaskReservation(t, clientID, key)
		})
	}
}
