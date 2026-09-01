package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

func TestH02AuthorizationDoesNotCrossRoutes(t *testing.T) {
	cfg := testConfig()
	cfg.Capabilities = map[string]config.Capability{
		"capability-alpha": {Backend: "backend-alpha"},
		"capability-beta":  {Backend: "backend-beta"},
	}
	cfg.Backends = map[string]config.BackendConfig{"backend-alpha": {Type: config.AdapterFake}, "backend-beta": {Type: config.AdapterFake}}
	cfg.Workloads = map[string]config.WorkloadPolicy{
		"workload-alpha": {PriorityClass: "normal", AllowedCapabilities: []string{"capability-alpha", "capability-beta"}, WorkloadTimeoutSeconds: 300},
		"workload-beta":  {PriorityClass: "normal", AllowedCapabilities: []string{"capability-alpha", "capability-beta"}, WorkloadTimeoutSeconds: 300},
	}
	env := newTestEnvWithConfig(t, cfg)
	tok, clientID := newClient(t, "h02-api")
	digest := cfg.TopologyDigest()
	for _, seed := range []struct{ name, capability string }{{"workload-alpha", "capability-alpha"}, {"workload-beta", "capability-beta"}} {
		name, capability := seed.name, seed.capability
		p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1, RetryDelaysSeconds: []int{1}, MaxOutputTokens: 2048, AllowedGeneration: []byte("{}"), WorkloadTimeoutSeconds: 300, PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400, MetadataRetentionSeconds: 2592000, AllowedCapabilities: []string{capability}, TopologyDigest: digest[:]})
		if err != nil {
			t.Fatal(err)
		}
		if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, p.Name); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		workload, capability string
		want                 int
	}{
		{"workload-alpha", "capability-alpha", http.StatusCreated},
		{"workload-beta", "capability-beta", http.StatusCreated},
		{"workload-alpha", "capability-beta", http.StatusForbidden},
		{"workload-beta", "capability-alpha", http.StatusForbidden},
		{"workload-unknown", "capability-alpha", http.StatusForbidden},
		{"workload-alpha", "capability-unknown", http.StatusForbidden},
	} {
		key := uniqueName("h02-cross-route-key")
		body := `{"workload":"` + tc.workload + `","capability":"` + tc.capability + `","messages":[{"role":"user","content":"synthetic"}],"response":{"kind":"text"}}`
		resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", body)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s/%s status = %d, want %d: %s", tc.workload, tc.capability, resp.StatusCode, tc.want, b)
		}
		if tc.want == http.StatusForbidden {
			var problem map[string]any
			if err := json.Unmarshal(b, &problem); err != nil || problem["code"] != codeForbidden || problem["detail"] != "workload or capability is not authorized" {
				t.Fatalf("cross-route response is not a problem document: %s", b)
			}
			var count int
			if err := adminDB.QueryRowContext(context.Background(), `SELECT count(*) FROM tasks WHERE client_id=$1 AND idempotency_key=$2`, clientID, key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("forbidden route request created %d task rows", count)
			}
			if err := adminDB.QueryRowContext(context.Background(), `SELECT count(*) FROM task_results r JOIN tasks t ON t.id = r.task_id WHERE t.client_id=$1 AND t.idempotency_key=$2`, clientID, key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("forbidden route request created %d durable results", count)
			}
		}
	}
}

func TestH02SchemaRequestAcceptanceAndRejectionRemainAtAPIBoundary(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "h02-schema")
	workload := grantWorkload(t, clientIDOf(t, tok), 10)
	valid := `{"workload":"` + workload + `","capability":"instruct","messages":[{"role":"user","content":"synthetic"}],"response":{"kind":"json_schema","schema":{"type":"object","required":["route"],"properties":{"route":{"type":"string"}}}}}`
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "h02-schema-key-0000001", "", valid)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("valid schema request status = %d: %s", resp.StatusCode, b)
	}
	invalid := `{"workload":"` + workload + `","capability":"instruct","messages":[{"role":"user","content":"synthetic"}],"response":{"kind":"json_schema"}}`
	resp, _ = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "h02-schema-key-0000002", "", invalid)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("schema-less request status = %d, want 422", resp.StatusCode)
	}
}

func TestH02IdempotencyIncludesRouteFields(t *testing.T) {
	cfg := testConfig()
	cfg.Backends = map[string]config.BackendConfig{
		"backend-alpha": {Type: config.AdapterFake},
		"backend-beta":  {Type: config.AdapterFake, Features: []string{"structured_output"}},
	}
	cfg.Capabilities = map[string]config.Capability{
		"capability-alpha": {Backend: "backend-alpha"},
		"capability-beta":  {Backend: "backend-beta"},
	}
	cfg.Workloads = map[string]config.WorkloadPolicy{
		"workload-alpha": {PriorityClass: "normal", AllowedCapabilities: []string{"capability-alpha"}, WorkloadTimeoutSeconds: 300},
		"workload-beta":  {PriorityClass: "normal", AllowedCapabilities: []string{"capability-beta"}, WorkloadTimeoutSeconds: 300},
	}
	env := newTestEnvWithConfig(t, cfg)
	tok, clientID := newClient(t, "h02-idempotency")
	digest := cfg.TopologyDigest()
	routes := []struct{ workload, capability string }{
		{"workload-alpha", "capability-alpha"}, {"workload-beta", "capability-beta"},
	}
	for _, route := range routes {
		p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
			Name: route.workload, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1,
			RetryDelaysSeconds: []int{1}, MaxOutputTokens: 2048, AllowedGeneration: []byte("{}"),
			WorkloadTimeoutSeconds: 300, PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
			MetadataRetentionSeconds: 2592000, AllowedCapabilities: []string{"capability-alpha", "capability-beta"}, TopologyDigest: digest[:],
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := adminStore.GrantWorkloadBySlug(context.Background(), clientID, p.Name); err != nil {
			t.Fatal(err)
		}
	}
	for _, route := range routes {
		first := strings.Replace(submission(route.workload), `"instruct"`, `"`+route.capability+`"`, 1)
		key := uniqueName("h02-idem-key")
		resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", first)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s first status=%d: %s", route.capability, resp.StatusCode, b)
		}
		id := taskID(t, b)
		resp, b = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", first)
		if resp.StatusCode != http.StatusCreated || taskID(t, b) != id {
			t.Fatalf("exact replay route=%s status=%d body=%s", route.capability, resp.StatusCode, b)
		}

		otherCapability := "capability-alpha"
		otherWorkload := "workload-alpha"
		if route.capability == otherCapability {
			otherCapability, otherWorkload = "capability-beta", "workload-beta"
		}
		changedCapability := strings.Replace(first, route.capability, otherCapability, 1)
		resp, _ = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", changedCapability)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("changed capability route=%s status=%d, want 409", route.capability, resp.StatusCode)
		}
		changedWorkload := strings.Replace(first, route.workload, otherWorkload, 1)
		resp, _ = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", changedWorkload)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("changed workload route=%s status=%d, want 409", route.capability, resp.StatusCode)
		}
	}
}

func TestH02RouteAndPrivateValuesStayOutOfObservability(t *testing.T) {
	env := newMetricsEnv(t, true)
	tok, clientID := newClient(t, "h02-observability")
	workload := grantWorkload(t, clientID, 10)
	const (
		prompt = "h02-private-prompt-4242"
		key    = "h02-private-key-4242"
	)
	body := strings.Replace(submission(workload), "Hello", prompt, 1)
	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key, "", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("route observability request status = %d", resp.StatusCode)
	}
	_, metricsBody := doReq(t, env.ts, http.MethodGet, "/metrics", "", "", "", "")
	for _, banned := range []string{prompt, key, tok.Value, tok.Identifier, "h02-observability", "capability-alpha", "capability-beta", "backend-alpha", "backend-beta", `route":"alpha`} {
		if strings.Contains(string(metricsBody), banned) {
			t.Errorf("metrics output leaks %q", banned)
		}
	}
	for _, banned := range []string{prompt, key, tok.Value, tok.Identifier, "backend-alpha", "backend-beta", "capability-alpha", "capability-beta"} {
		if strings.Contains(env.logs.String(), banned) {
			t.Errorf("request logs leak %q", banned)
		}
	}
}
