package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

const key1 = "idem-key-0001"

func taskID(t *testing.T, b []byte) string {
	t.Helper()
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &task); err != nil {
		t.Fatalf("decode task body: %v", err)
	}
	return task.ID
}

// --- creation and idempotency ---------------------------------------------

func TestCreateTaskCreated(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "creator")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key1, "", submission(wl))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", resp.StatusCode, b)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/v1/tasks/") {
		t.Errorf("missing Location header: %q", loc)
	}
	var task struct {
		ID         string `json:"id"`
		Workload   string `json:"workload"`
		Capability string `json:"capability"`
		Status     string `json:"status"`
		Attempts   int    `json:"attempts"`
	}
	if err := json.Unmarshal(b, &task); err != nil {
		t.Fatal(err)
	}
	if task.Workload != wl || task.Capability != "instruct" || task.Status != "queued" || task.Attempts != 0 {
		t.Errorf("task mismatch: %+v", task)
	}
	// Persisted: a read returns the same state.
	resp2, b2 := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+task.ID, tok.Value, "", "", "")
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(b2), `"queued"`) {
		t.Errorf("read after create: %d %s", resp2.StatusCode, b2)
	}
}

func TestCreateTaskIdempotentReplay(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "replayer")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	_, b1 := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "idem-key-replay", "", submission(wl))
	_, b2 := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "idem-key-replay", "", submission(wl))
	if taskID(t, b1) != taskID(t, b2) {
		t.Error("same key + same request must replay the same task")
	}
	// Different request, same key: conflict.
	other := fmt.Sprintf(`{"workload": %q, "capability": "instruct",
		"messages": [{"role": "user", "content": "different"}],
		"response": {"kind": "text"}}`, wl)
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "idem-key-replay", "", other)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409; body %s", resp.StatusCode, b)
	}
	if code := decodeProblem(t, b)["code"]; code != codeIdempotencyConflict {
		t.Errorf("code = %v, want %s", code, codeIdempotencyConflict)
	}
}

func TestCreateTaskKeysScopedPerClient(t *testing.T) {
	env := newTestEnv(t)
	tokA, _ := newClient(t, "scoper-a")
	tokB, _ := newClient(t, "scoper-b")
	wlA := grantWorkload(t, clientIDOf(t, tokA), 10)
	wlB := grantWorkload(t, clientIDOf(t, tokB), 10)

	_, bA := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tokA.Value, "shared-key-01", "", submission(wlA))
	_, bB := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tokB.Value, "shared-key-01", "", submission(wlB))
	if taskID(t, bA) == taskID(t, bB) {
		t.Error("different clients must not share idempotency scope")
	}
}

func TestCreateTaskConcurrentSameKey(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "concurrent")
	wl := grantWorkload(t, clientID, 100)

	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "concurrent-key-01", "", submission(wl))
			if resp.StatusCode != http.StatusCreated {
				t.Errorf("goroutine %d: status %d", i, resp.StatusCode)
				return
			}
			mu.Lock()
			ids[i] = taskID(t, b)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	first := ids[0]
	for i, id := range ids {
		if id == "" {
			t.Errorf("goroutine %d got no id", i)
			continue
		}
		if id != first {
			t.Errorf("goroutine %d id %s != %s", i, id, first)
		}
	}
}

// --- authentication and authorization --------------------------------------

func TestCreateTaskRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "authreq")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	cases := []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"malformed", "not-a-bearer-token"},
		{"wrong token", "lbk_" + strings.Repeat("A", 43)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", c.token, key1, "", submission(wl))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401; body %s", resp.StatusCode, b)
			}
		})
	}
}

func TestCreateTaskDisabledClient(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "disabled")
	wl := grantWorkload(t, clientID, 10)
	adminStore.SetClientEnabled(context.Background(), clientID, false)
	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, key1, "", submission(wl))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("disabled client status = %d, want 401", resp.StatusCode)
	}
}

func TestCreateTaskAuthorization(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "authz")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	t.Run("unknown workload", func(t *testing.T) {
		resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "authz-key-01", "", submission("no-such-workload"))
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
		_ = b
	})
	t.Run("not granted", func(t *testing.T) {
		other := uniqueName("otherwl")
		adminStore.UpsertWorkloadPolicy(context.Background(), postgres.WorkloadPolicy{
			Name: other, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 3,
			RetryDelaysSeconds: []int{1, 5}, MaxOutputTokens: 2048, AllowedGeneration: []byte("{}"),
			RequiredFeatures: []string{}, PayloadRetentionSeconds: 3600,
			ResultRetentionSeconds: 86400, MetadataRetentionSeconds: 2592000,
		})
		resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "authz-key-02", "", submission(other))
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("unknown capability", func(t *testing.T) {
		body := fmt.Sprintf(`{"workload": %q, "capability": "magic",
			"messages": [{"role": "user", "content": "hi"}],
			"response": {"kind": "text"}}`, wl)
		resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "authz-key-03", "", body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", resp.StatusCode)
		}
		_ = b
	})
}

// --- input validation ------------------------------------------------------

func TestCreateTaskRejectsInvalidInput(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "invalid")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	cases := []struct {
		name   string
		key    string
		prefer string
		body   string
		status int
	}{
		{"missing idempotency key", "", "", submission(wl), http.StatusBadRequest},
		{"short key", "short", "", submission(wl), http.StatusBadRequest},
		{"unknown field", key1, "", `{"workload": "x", "capability": "instruct", "model": "gpt-x", "messages": [{"role": "user", "content": "hi"}], "response": {"kind": "text"}}`, http.StatusUnprocessableEntity},
		{"bad prefer", key1, "wait=abc", submission(wl), http.StatusBadRequest},
		{"wait too long", key1, "wait=31", submission(wl), http.StatusUnprocessableEntity},
		{"trailing content", key1, "", submission(wl) + " {}", http.StatusBadRequest},
		{"empty messages", key1, "", fmt.Sprintf(`{"workload": %q, "capability": "instruct", "messages": [], "response": {"kind": "text"}}`, wl), http.StatusUnprocessableEntity},
		{"json_schema without schema", key1, "", fmt.Sprintf(`{"workload": %q, "capability": "instruct", "messages": [{"role": "user", "content": "hi"}], "response": {"kind": "json_schema"}}`, wl), http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, c.key, c.prefer, c.body)
			if resp.StatusCode != c.status {
				t.Errorf("status = %d, want %d; body %s", resp.StatusCode, c.status, b)
			}
			var p map[string]any
			if err := json.Unmarshal(b, &p); err != nil {
				t.Errorf("not a problem document: %s", b)
			} else if p["code"] == "" {
				t.Errorf("problem missing code: %s", b)
			}
		})
	}
}

func TestCreateTaskBodyTooLarge(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "huge")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)
	big := fmt.Sprintf(`{"workload": %q, "capability": "instruct",
		"messages": [{"role": "user", "content": %q}],
		"response": {"kind": "text"}}`, wl, strings.Repeat("x", 600*1024))
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "huge-key-01", "", big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body %s", resp.StatusCode, b)
	}
}

func TestCreateTaskGenerationPolicy(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "genpolicy")
	name := uniqueName("genwl")
	adminStore.UpsertWorkloadPolicy(context.Background(), postgres.WorkloadPolicy{
		Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 3,
		RetryDelaysSeconds: []int{1, 5}, MaxOutputTokens: 2048,
		AllowedGeneration:        []byte(`{"max_tokens": {"max": 100}}`),
		RequiredFeatures:         []string{},
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
	})
	pol, _ := adminStore.GetWorkloadPolicyByName(context.Background(), name)
	adminStore.GrantWorkload(context.Background(), clientID, pol.ID)

	over := fmt.Sprintf(`{"workload": %q, "capability": "instruct",
		"messages": [{"role": "user", "content": "hi"}],
		"generation": {"max_tokens": 200},
		"response": {"kind": "text"}}`, name)
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "gen-key-01", "", over)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body %s", resp.StatusCode, b)
	}

	under := fmt.Sprintf(`{"workload": %q, "capability": "instruct",
		"messages": [{"role": "user", "content": "hi"}],
		"generation": {"max_tokens": 50},
		"response": {"kind": "text"}}`, name)
	resp, _ = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "gen-key-02", "", under)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("in-bounds status = %d, want 201", resp.StatusCode)
	}
}

func TestCreateTaskAdmission(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "admission")
	wl := grantWorkload(t, clientID, 1)

	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "adm-key-01", "", submission(wl))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first task status = %d, want 201", resp.StatusCode)
	}
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "adm-key-02", "", submission(wl))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second task status = %d, want 429; body %s", resp.StatusCode, b)
	}
	if code := decodeProblem(t, b)["code"]; code != codeAdmissionLimited {
		t.Errorf("code = %v, want %s", code, codeAdmissionLimited)
	}
}

// --- reads, scoping, results -----------------------------------------------

func TestGetTaskScopedToClient(t *testing.T) {
	env := newTestEnv(t)
	tokA, clientA := newClient(t, "scoped-a")
	tokB, _ := newClient(t, "scoped-b")
	wlA := grantWorkload(t, clientA, 10)

	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tokA.Value, "scope-key-01", "", submission(wlA))
	id := taskID(t, b)

	resp, _ := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+id, tokB.Value, "", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-client read status = %d, want 404", resp.StatusCode)
	}
	resp, _ = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+id, tokA.Value, "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("owner read status = %d, want 200", resp.StatusCode)
	}
}

func TestResultStatuses(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "results")
	wl := grantWorkload(t, clientID, 10)
	ctx := context.Background()

	// pending
	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "res-key-pending", "", submission(wl))
	pendingID := taskID(t, b)
	resp, b := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+pendingID+"/result", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"pending"`) {
		t.Errorf("pending result: %d %s", resp.StatusCode, b)
	}

	// succeeded
	_, b = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "res-key-ok", "", submission(wl))
	okID, _ := uuid.Parse(taskID(t, b))
	claimAndFinish(t, okID)
	resp, b = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+okID.String()+"/result", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("succeeded result status: %d %s", resp.StatusCode, b)
	}
	var envResp struct {
		Status string `json:"status"`
		Result *struct {
			Content      string `json:"content"`
			FinishReason string `json:"finish_reason"`
			Usage        struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &envResp); err != nil {
		t.Fatal(err)
	}
	if envResp.Status != "succeeded" || envResp.Result == nil ||
		envResp.Result.Content != "hello" || envResp.Result.FinishReason != "stop" ||
		envResp.Result.Usage.TotalTokens != 15 {
		t.Errorf("succeeded envelope mismatch: %s", b)
	}

	// failed
	_, b = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "res-key-fail", "", submission(wl))
	failID, _ := uuid.Parse(taskID(t, b))
	claimAndFail(t, failID)
	resp, b = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+failID.String()+"/result", tok.Value, "", "", "")
	var failEnv struct {
		Status string `json:"status"`
		Error  *struct {
			Class string `json:"class"`
			Code  string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &failEnv); err != nil {
		t.Fatal(err)
	}
	if failEnv.Status != "failed" || failEnv.Error == nil ||
		failEnv.Error.Class != "output_invalid" || failEnv.Error.Code != "output_invalid" {
		t.Errorf("failed envelope mismatch: %s", b)
	}

	// expired: succeeded task whose result row was purged
	_, b = doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "res-key-expired", "", submission(wl))
	expiredID, _ := uuid.Parse(taskID(t, b))
	claimAndFinish(t, expiredID)
	if _, err := adminStore.Pool().Exec(ctx,
		"DELETE FROM task_results WHERE task_id = $1", expiredID); err != nil {
		t.Fatal(err)
	}
	resp, b = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+expiredID.String()+"/result", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"expired"`) {
		t.Errorf("expired result: %d %s", resp.StatusCode, b)
	}
}

// claimAndFinish claims the task and simulates worker completion.
func claimAndFinish(t *testing.T, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tw, err := rtStore.ClaimNextQueued(ctx, uuid.New(), 60*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if tw.ID != id {
		// The claim grabbed a different task (parallel tests); claim ours.
		for {
			tw, err = rtStore.ClaimNextQueued(ctx, uuid.New(), 60*time.Second)
			if err != nil {
				t.Fatalf("claim loop: %v", err)
			}
			if tw.ID == id {
				break
			}
		}
	}
	if err := rtStore.TransitionTask(ctx, id, domain.TaskRunning, domain.TaskSucceeded); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := rtStore.SaveResult(ctx, id, "hello", "stop", 10, 5, 15); err != nil {
		t.Fatalf("save result: %v", err)
	}
}

func claimAndFail(t *testing.T, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for {
		tw, err := rtStore.ClaimNextQueued(ctx, uuid.New(), 60*time.Second)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if tw.ID == id {
			break
		}
	}
	if err := rtStore.TransitionTask(ctx, id, domain.TaskRunning, domain.TaskFailed); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := rtStore.MarkAttemptFailed(ctx, id, 1, "output_invalid", "output_invalid"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
}

// --- wait ------------------------------------------------------------------

func TestWaitReturnsTerminalWithinBudget(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "waiter")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)
	ctx := context.Background()

	// Create without wait, finish the task, then replay with wait: the
	// idempotent replay must return the terminal result as 200.
	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "wait-key-01", "", submission(wl))
	id, _ := uuid.Parse(taskID(t, b))
	claimAndFinish(t, id)

	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "wait-key-01", "wait=5", submission(wl))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), `"succeeded"`) {
		t.Errorf("wait response not terminal: %s", b)
	}
	_ = ctx
}

func TestWaitExpiresWith202(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "wait-202")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	start := time.Now()
	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "wait-key-02", "wait=1", submission(wl))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", resp.StatusCode, b)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("wait budget not honored")
	}
	if resp.Header.Get("Retry-After") == "" || resp.Header.Get("Location") == "" {
		t.Error("202 must carry Retry-After and Location")
	}
	// Task is still queued: the API never executes anything.
	resp2, b2 := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+taskID(t, b), tok.Value, "", "", "")
	if resp2.StatusCode != http.StatusOK || !strings.Contains(string(b2), `"queued"`) {
		t.Errorf("task must remain queued: %d %s", resp2.StatusCode, b2)
	}
}

func TestWaitDisconnectLeavesTaskQueued(t *testing.T) {
	env := newTestEnv(t)
	tok, clientID := newClient(t, "wait-disc")
	wl := grantWorkload(t, clientID, 10)

	// Send a wait=30 POST and cancel the client context mid-wait.
	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/v1/tasks", strings.NewReader(submission(wl)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Value)
	req.Header.Set("Idempotency-Key", "wait-key-03")
	req.Header.Set("Prefer", "wait=30")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	_, _ = env.ts.Client().Do(req) // client error is expected

	// The task must exist and remain queued: disconnect never cancels work.
	tasks, err := rtStore.ListTasksByClient(context.Background(), clientID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(tasks))
	}
	if tasks[0].State != domain.TaskQueued {
		t.Errorf("task state after disconnect = %s, want queued", tasks[0].State)
	}
	resp, b := doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+tasks[0].ID.String(), tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"queued"`) {
		t.Errorf("task after disconnect: %d %s", resp.StatusCode, b)
	}
}

// --- cancellation ----------------------------------------------------------

func TestCancelQueuedTask(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "cancel-q")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "cancel-key-01", "", submission(wl))
	id := taskID(t, b)

	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks/"+id+"/cancel", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"cancelled"`) {
		t.Fatalf("cancel status: %d %s", resp.StatusCode, b)
	}
	// Idempotent second cancel.
	resp, _ = doReq(t, env.ts, http.MethodPost, "/v1/tasks/"+id+"/cancel", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("second cancel status = %d, want 200", resp.StatusCode)
	}
	// Result reports cancelled.
	resp, b = doReq(t, env.ts, http.MethodGet, "/v1/tasks/"+id+"/result", tok.Value, "", "", "")
	if !strings.Contains(string(b), `"cancelled"`) {
		t.Errorf("result after cancel: %s", b)
	}
}

func TestCancelRunningRecordsIntent(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "cancel-r")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "cancel-key-02", "", submission(wl))
	id, _ := uuid.Parse(taskID(t, b))
	ctx := context.Background()
	for {
		tw, err := rtStore.ClaimNextQueued(ctx, uuid.New(), 60*time.Second)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if tw.ID == id {
			break
		}
	}

	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks/"+id.String()+"/cancel", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"running"`) {
		t.Fatalf("cancel running status: %d %s", resp.StatusCode, b)
	}
	task, err := rtStore.GetTaskByClient(ctx, id, clientIDOf(t, tok))
	if err != nil {
		t.Fatal(err)
	}
	if task.CancelRequestedAt == nil {
		t.Error("cancel intent not recorded")
	}
}

func TestCancelTerminalConflicts(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := newClient(t, "cancel-t")
	wl := grantWorkload(t, clientIDOf(t, tok), 10)

	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tok.Value, "cancel-key-03", "", submission(wl))
	id, _ := uuid.Parse(taskID(t, b))
	claimAndFinish(t, id)

	resp, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks/"+id.String()+"/cancel", tok.Value, "", "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("terminal cancel status = %d, want 409; body %s", resp.StatusCode, b)
	}
}

func TestCancelCrossClientNotFound(t *testing.T) {
	env := newTestEnv(t)
	tokA, clientA := newClient(t, "cancel-a")
	tokB, _ := newClient(t, "cancel-b")
	wlA := grantWorkload(t, clientA, 10)

	_, b := doReq(t, env.ts, http.MethodPost, "/v1/tasks", tokA.Value, "cancel-key-04", "", submission(wlA))
	id := taskID(t, b)
	resp, _ := doReq(t, env.ts, http.MethodPost, "/v1/tasks/"+id+"/cancel", tokB.Value, "", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-client cancel status = %d, want 404", resp.StatusCode)
	}
}

// --- health and readiness --------------------------------------------------

func TestHealthAndReady(t *testing.T) {
	env := newTestEnv(t)
	resp, b := doReq(t, env.ts, http.MethodGet, "/health", "", "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"ok"`) {
		t.Errorf("health: %d %s", resp.StatusCode, b)
	}
	resp, b = doReq(t, env.ts, http.MethodGet, "/ready", "", "", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"ready":true`) {
		t.Errorf("ready: %d %s", resp.StatusCode, b)
	}
}

func TestReadyFailsWhenDatabaseUnreachable(t *testing.T) {
	// A server bound to a closed store must answer 503 on /ready.
	closed, err := postgres.Open(context.Background(), testAdminURL(), postgres.Options{MaxConns: 1, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	logger := discardLogger()
	srv := New(Options{Store: closed, Config: testConfig(), Logger: logger})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, b := doReq(t, ts, http.MethodGet, "/ready", "", "", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ready with closed store = %d, want 503; body %s", resp.StatusCode, b)
	}
}
