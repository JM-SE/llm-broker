package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/api"
	"github.com/JM-SE/llm-broker/internal/auth"
	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// newAPIServer builds the full API stack over the runtime store with an
// in-memory config and returns the test server plus the config used.
func newAPIServer(t *testing.T, capabilities map[string]config.Capability, backends map[string]config.BackendConfig) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		Version:      1,
		API:          config.APIConfig{Listen: "127.0.0.1:0", BodyLimitBytes: 524288},
		Capabilities: capabilities,
		Backends:     backends,
	}
	verifier := auth.NewVerifier(func(ctx context.Context, identifier string) (auth.Identity, []byte, bool, error) {
		c, err := rtStore.GetClientByIdentifier(ctx, identifier)
		if err != nil {
			if err == postgres.ErrNotFound {
				return auth.Identity{}, nil, false, nil
			}
			return auth.Identity{}, nil, false, err
		}
		return auth.Identity{ClientID: c.ID, Name: c.Name}, c.CredentialDigest, c.Enabled, nil
	})
	srv := api.New(api.Options{Store: rtStore, Config: cfg, Verifier: verifier})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// apiClient provisions a client and returns its bearer token.
func apiClient(t *testing.T, workload string) (auth.Token, int64) {
	t.Helper()
	tok, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c, err := rtStore.CreateClient(context.Background(), postgres.CreateClientParams{
		Name:                 uniqueName("e2eclient"),
		CredentialIdentifier: tok.Identifier,
		CredentialDigest:     tok.Digest[:],
		Enabled:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok, c.ID
}

func postTask(t *testing.T, ts *httptest.Server, tok auth.Token, workload string, wait int) (*http.Response, []byte) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"workload": "%s", "capability": "instruct",
		"messages": [{"role": "user", "content": "hello"}],
		"response": {"kind": "text"}}`, workload))
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/tasks", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Value)
	req.Header.Set("Idempotency-Key", uniqueName("e2e-key-0000000000000"))
	if wait > 0 {
		req.Header.Set("Prefer", fmt.Sprintf("wait=%d", wait))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestE2EAPIWorkerFake(t *testing.T) {
	clearTasks(t)
	// Shared workload policy + client.
	policy := mustPolicy(t, "e2e-wl", 3, nil)
	tok, clientID := apiClient(t, "default")
	mustGrant(t, clientID, policy.ID)

	capabilities := map[string]config.Capability{"instruct": {Backend: "fake"}}
	backends := map[string]config.BackendConfig{"fake": {Type: config.AdapterFake}}
	ts := newAPIServer(t, capabilities, backends)

	w, _, _ := newWorkerEnv(t, "none", 0)
	startWorker(t, w)

	// POST with a bounded wait returns the completed result.
	resp, data := postTask(t, ts, tok, policy.Name, 30)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, data)
	}
	var envelope struct {
		Status string `json:"status"`
		Result *struct {
			Content      string `json:"content"`
			FinishReason string `json:"finish_reason"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, data)
	}
	if envelope.Status != "succeeded" || envelope.Result == nil {
		t.Fatalf("envelope = %s", data)
	}
	if len(envelope.Result.Content) == 0 {
		t.Error("empty result content")
	}
}

// TestE2ENoOverlappingInference runs two workers against an OpenAI-compatible
// test backend and asserts the backend never sees two concurrent calls.
func TestE2ENoOverlappingInference(t *testing.T) {
	clearTasks(t)
	t.Setenv("LLM_BROKER_TEST_BACKEND_KEY", "test-secret-token-42")

	var active, maxActive atomic.Int32
	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maxActive.Load()
			if cur <= seen || maxActive.CompareAndSwap(seen, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(backendSrv.Close)

	policy := mustPolicy(t, "e2e-nolap", 3, nil)
	client := mustClient(t)
	mustGrant(t, client.ID, policy.ID)
	payload := payloadFor(t, policy.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "hello"}}, textResponse)

	capabilities := map[string]config.Capability{"instruct": {Backend: "oc"}}
	backends := map[string]config.BackendConfig{"oc": {
		Type:          config.AdapterOpenAICompatible,
		BaseURL:       backendSrv.URL,
		Model:         "operator-model-alias",
		CredentialEnv: "LLM_BROKER_TEST_BACKEND_KEY",
	}}

	mkWorker := func() *Worker {
		policy := scheduler.DefaultPolicy()
		policy.HeartbeatInterval = 100 * time.Millisecond
		leader := scheduler.NewLeader(rtStore, uuid.New(), policy.HeartbeatInterval, nil)
		sched := scheduler.New(rtStore, policy, leader)
		return New(Options{
			Store:        rtStore,
			Scheduler:    sched,
			Leader:       leader,
			Registry:     backend.DefaultRegistry,
			Capabilities: capabilities,
			Backends:     backends,
			Policy:       policy,
			ClaimPoll:    50 * time.Millisecond,
		})
	}
	w1 := mkWorker()
	w2 := mkWorker()
	startWorker(t, w1)
	startWorker(t, w2)

	var ids []uuid.UUID
	for i := 0; i < 6; i++ {
		ids = append(ids, mustEnqueue(t, client.ID, policy.ID, uniqueName("nl-key-000000000000000"), payload))
	}
	for _, id := range ids {
		waitForState(t, id, domain.TaskSucceeded, 15*time.Second)
	}
	if got := maxActive.Load(); got != 1 {
		t.Errorf("max concurrent backend calls = %d, want 1", got)
	}
}
