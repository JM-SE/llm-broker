package worker

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/scheduler"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

type h02RouteSpy struct {
	marker         string
	calls          atomic.Int32
	active         atomic.Int32
	max            atomic.Int32
	bad            bool
	started        chan struct{}
	release        chan struct{}
	once           sync.Once
	transient      atomic.Int32
	errorClass     domain.ErrorClass
	errorCode      string
	shared         *h02SharedInference
	cancelObserved chan struct{}
	cancelOnce     sync.Once
}

type h02SharedInference struct {
	active atomic.Int32
	max    atomic.Int32
}

func (s *h02RouteSpy) Type() string { return config.AdapterFake }
func (s *h02RouteSpy) Infer(ctx context.Context, _ *backend.Request) (*backend.Response, error) {
	s.calls.Add(1)
	cur := s.active.Add(1)
	defer s.active.Add(-1)
	if s.shared != nil {
		shared := s.shared.active.Add(1)
		defer s.shared.active.Add(-1)
		for {
			old := s.shared.max.Load()
			if shared <= old || s.shared.max.CompareAndSwap(old, shared) {
				break
			}
		}
	}
	for {
		old := s.max.Load()
		if cur <= old || s.max.CompareAndSwap(old, cur) {
			break
		}
	}
	if s.started != nil {
		s.once.Do(func() { close(s.started) })
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			if s.cancelObserved != nil {
				s.cancelOnce.Do(func() { close(s.cancelObserved) })
			}
			return nil, ctx.Err()
		}
	}
	if s.transient.Load() > 0 {
		s.transient.Add(-1)
		return nil, backend.NewError(s.errorClass, s.errorCode, "deterministic test failure")
	}
	content := s.marker
	if s.bad {
		content = `{"wrong":true}`
	}
	return &backend.Response{Content: content, FinishReason: domain.FinishStop}, nil
}

func h02WorkerConfig() (*config.Config, *h02RouteSpy, *h02RouteSpy) {
	shared := &h02SharedInference{}
	a, b := &h02RouteSpy{marker: `{"route":"alpha"}`, shared: shared}, &h02RouteSpy{marker: `{"route":"beta"}`, shared: shared}
	return &config.Config{
		Version: config.Version,
		Backends: map[string]config.BackendConfig{
			"backend-alpha": {Type: config.AdapterFake},
			"backend-beta":  {Type: config.AdapterFake, Features: []string{"structured_output"}},
		},
		Capabilities: map[string]config.Capability{
			"capability-alpha": {Backend: "backend-alpha"}, "capability-beta": {Backend: "backend-beta"},
		},
	}, a, b
}

func h02Worker(t *testing.T, cfg *config.Config, a, b *h02RouteSpy) *Worker {
	t.Helper()
	digest := cfg.TopologyDigest()
	policy := scheduler.DefaultPolicy()
	policy.HeartbeatInterval = 100 * time.Millisecond
	leader := scheduler.NewLeader(rtStore, uuid.New(), policy.HeartbeatInterval, nil)
	sched := scheduler.New(rtStore, policy, leader)
	registry := backend.NewRegistry()
	registry.Register(config.AdapterFake, func(c config.BackendConfig) (backend.Adapter, error) {
		for _, feature := range c.Features {
			if feature == "structured_output" {
				return b, nil
			}
		}
		return a, nil
	})
	return New(Options{Store: rtStore, Scheduler: sched, Leader: leader, Registry: registry,
		Capabilities: cfg.Capabilities, Backends: cfg.Backends,
		ConfiguredWorkloads: map[string][32]byte{"workload-alpha": digest, "workload-beta": digest},
		Policy:              policy, ClaimPoll: 10 * time.Millisecond})
}

func h02Policy(t *testing.T, name, capability string, digest [32]byte, features []string) postgres.WorkloadPolicy {
	t.Helper()
	p, err := testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
		Name: uniqueName(name), PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 1,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 2048, AllowedGeneration: []byte("{}"), RequiredFeatures: features,
		WorkloadTimeoutSeconds: 30, PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000, AllowedCapabilities: []string{capability}, TopologyDigest: digest[:],
	})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

func h02Enqueue(t *testing.T, client int64, p postgres.WorkloadPolicy, capability string, rc domain.ResponseContract) uuid.UUID {
	t.Helper()
	messages := []domain.Message{{Role: domain.RoleUser, Content: "synthetic"}}
	submission := domain.TaskSubmission{Workload: p.Name, Capability: capability, Messages: messages, Response: rc}
	payload, err := submission.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize submission: %v", err)
	}
	hashHex, err := submission.RequestHash()
	if err != nil {
		t.Fatalf("hash submission: %v", err)
	}
	hash, err := hex.DecodeString(hashHex)
	if err != nil {
		t.Fatalf("decode submission hash: %v", err)
	}
	var id uuid.UUID
	if err := adminDB.QueryRowContext(context.Background(), `INSERT INTO tasks (client_id, workload_id, capability, idempotency_key, request_hash) VALUES ($1,$2,$3,$4,$5) RETURNING id`, client, p.ID, capability, uniqueName("h02-key"), hash).Scan(&id); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := adminDB.ExecContext(context.Background(), `INSERT INTO task_payloads (task_id, payload) VALUES ($1,$2)`, id, string(payload)); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return id
}

func h02Claim(t *testing.T, w *Worker, taskID uuid.UUID) postgres.TaskWithPayload {
	t.Helper()
	if ok, err := w.leader.TryAcquire(context.Background()); err != nil || !ok {
		t.Fatalf("acquire leadership: %v, %v", ok, err)
	}
	res, err := w.sched.ClaimNext(context.Background())
	w.leader.Release()
	if err != nil || res.NoTask || res.Task.ID != taskID {
		t.Fatalf("claim: task=%s want=%s err=%v no_task=%v", res.Task.ID, taskID, err, res.NoTask)
	}
	return res.Task
}

func h02RouteTask(t *testing.T, cfg *config.Config, w *Worker, client postgres.Client, capability string, features []string, rc domain.ResponseContract, maxAttempts int) (uuid.UUID, postgres.WorkloadPolicy) {
	t.Helper()
	digest := cfg.TopologyDigest()
	p := h02Policy(t, "h02-"+capability, capability, digest, features)
	if maxAttempts > 1 {
		var err error
		p, err = testsupport.ApplyPolicy(context.Background(), adminStore, postgres.WorkloadPolicy{
			Name: uniqueName("h02-retry"), PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: maxAttempts,
			RetryDelaysSeconds: []int{1}, MaxOutputTokens: 2048, AllowedGeneration: []byte("{}"), RequiredFeatures: features,
			WorkloadTimeoutSeconds: 30, PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
			MetadataRetentionSeconds: 2592000, AllowedCapabilities: []string{capability}, TopologyDigest: digest[:],
		})
		if err != nil {
			t.Fatalf("retry policy: %v", err)
		}
	}
	w.configuredWorkloads[p.Name] = digest
	mustGrant(t, client.ID, p.ID)
	return h02Enqueue(t, client.ID, p, capability, rc), p
}

func TestH02WorkerRoutesMarkersAndStructuredOutput(t *testing.T) {
	clearTasks(t)
	cfg, alpha, beta := h02WorkerConfig()
	w := h02Worker(t, cfg, alpha, beta)
	client := mustClient(t)
	da, db := cfg.TopologyDigest(), cfg.TopologyDigest()
	pa := h02Policy(t, "workload-alpha", "capability-alpha", da, nil)
	pb := h02Policy(t, "workload-beta", "capability-beta", db, []string{"structured_output"})
	w.configuredWorkloads[pa.Name] = da
	w.configuredWorkloads[pb.Name] = db
	mustGrant(t, client.ID, pa.ID)
	mustGrant(t, client.ID, pb.ID)
	schema, err := domain.NewSchemaContract([]byte(`{"type":"object","required":["route"],"properties":{"route":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	aID := h02Enqueue(t, client.ID, pa, "capability-alpha", domain.ResponseContract{Kind: domain.ResponseText})
	bID := h02Enqueue(t, client.ID, pb, "capability-beta", domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: schema})
	if err := w.execute(context.Background(), h02Claim(t, w, aID)); err != nil {
		t.Fatal(err)
	}
	if err := w.execute(context.Background(), h02Claim(t, w, bID)); err != nil {
		t.Fatal(err)
	}
	if alpha.calls.Load() != 1 || beta.calls.Load() != 1 {
		t.Fatalf("route calls alpha=%d beta=%d", alpha.calls.Load(), beta.calls.Load())
	}
	if alpha.marker == beta.marker {
		t.Fatal("route markers are not distinguishable")
	}
	if alpha.max.Load() > 1 || beta.max.Load() > 1 {
		t.Fatal("a route entered inference concurrently")
	}
	for _, tc := range []struct {
		id     uuid.UUID
		marker string
	}{{aID, alpha.marker}, {bID, beta.marker}} {
		result, err := rtStore.GetResultByTaskID(context.Background(), tc.id)
		if err != nil || result.Content != tc.marker {
			t.Fatalf("persisted route result=%+v err=%v want marker %q", result, err, tc.marker)
		}
	}
}

func TestH02WorkerOutputValidationDoesNotPersistSuccess(t *testing.T) {
	clearTasks(t)
	cfg, alpha, beta := h02WorkerConfig()
	beta.marker = `{"route":"alpha"}`
	w := h02Worker(t, cfg, alpha, beta)
	client := mustClient(t)
	digest := cfg.TopologyDigest()
	p := h02Policy(t, "workload-beta", "capability-beta", digest, []string{"structured_output"})
	w.configuredWorkloads[p.Name] = digest
	mustGrant(t, client.ID, p.ID)
	schema, err := domain.NewSchemaContract([]byte(`{"type":"object","required":["route"],"properties":{"route":{"type":"string","enum":["beta"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := h02Enqueue(t, client.ID, p, "capability-beta", domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: schema})
	err = w.execute(context.Background(), h02Claim(t, w, id))
	if err != nil {
		t.Fatalf("cross-route marker execution returned unexpected error: %v", err)
	}
	task, err := rtStore.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskFailed || beta.calls.Load() != 1 || alpha.calls.Load() != 0 {
		t.Fatalf("task=%s calls alpha=%d beta=%d", task.State, alpha.calls.Load(), beta.calls.Load())
	}
	if _, err := rtStore.GetResultByTaskID(context.Background(), id); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("invalid output persisted: %v", err)
	}
}

func h02Schema(t *testing.T) *domain.SchemaContract {
	t.Helper()
	s, err := domain.NewSchemaContract([]byte(`{"type":"object","required":["route"],"properties":{"route":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestH02FeatureFenceLeavesAuthorizedRecoveryWithoutInfer(t *testing.T) {
	clearTasks(t)
	cfg, alpha, beta := h02WorkerConfig()
	w := h02Worker(t, cfg, alpha, beta)
	client := mustClient(t)
	id, _ := h02RouteTask(t, cfg, w, client, "capability-alpha", []string{"structured_output"}, domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: h02Schema(t)}, 1)
	claimed := h02Claim(t, w, id)
	err := w.execute(context.Background(), claimed)
	if err == nil {
		t.Fatal("feature fence unexpectedly allowed execution")
	}
	if alpha.calls.Load() != 0 || beta.calls.Load() != 0 {
		t.Fatalf("inference calls alpha=%d beta=%d", alpha.calls.Load(), beta.calls.Load())
	}
	if !errors.Is(err, postgres.ErrPreflightOperational) {
		t.Fatalf("feature fence error = %v, want operational preflight failure", err)
	}
	if task, err := rtStore.GetTask(context.Background(), id); err != nil || task.State != domain.TaskRunning {
		t.Fatalf("feature-fenced task=%v err=%v, want running", task.State, err)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != postgres.AttemptPhaseAuthorized {
		t.Fatalf("feature-fenced attempt=%+v, want authorized", attempt)
	}
}

func TestH02CancellationQueuedAndDuringInferBothRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, capability string
		features         []string
		rc               domain.ResponseContract
	}{
		{"alpha", "capability-alpha", nil, domain.ResponseContract{Kind: domain.ResponseText}},
		{"beta", "capability-beta", []string{"structured_output"}, domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: h02Schema(t)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTasks(t)
			cfg, spy, other := h02WorkerConfig()
			if tc.capability == "capability-beta" {
				spy, other = other, spy
			}
			w := h02Worker(t, cfg, alphaFor(tc.capability, spy, other), betaFor(tc.capability, spy, other))
			client := mustClient(t)
			id, _ := h02RouteTask(t, cfg, w, client, tc.capability, tc.features, tc.rc, 1)
			if _, err := rtStore.CancelQueuedTask(context.Background(), id, client.ID); err != nil {
				t.Fatal(err)
			}
			if task, err := rtStore.GetTask(context.Background(), id); err != nil || task.State != domain.TaskCancelled {
				t.Fatalf("queued cancel task=%v err=%v", task.State, err)
			}
			if spy.calls.Load() != 0 || other.calls.Load() != 0 {
				t.Fatalf("queued cancellation invoked routes selected=%d other=%d", spy.calls.Load(), other.calls.Load())
			}
			if _, err := rtStore.GetResultByTaskID(context.Background(), id); !errors.Is(err, postgres.ErrNotFound) {
				t.Fatalf("queued cancellation persisted result: %v", err)
			}

			clearTasks(t)
			started, release := make(chan struct{}), make(chan struct{})
			spy = &h02RouteSpy{marker: `{"route":"held"}`, started: started, release: release}
			other = &h02RouteSpy{marker: `{"route":"other"}`}
			w = h02Worker(t, cfg, alphaFor(tc.capability, spy, other), betaFor(tc.capability, spy, other))
			client = mustClient(t)
			id, _ = h02RouteTask(t, cfg, w, client, tc.capability, tc.features, tc.rc, 1)
			claimed := h02Claim(t, w, id)
			done := make(chan error, 1)
			go func() { done <- w.execute(context.Background(), claimed) }()
			<-started
			if _, err := rtStore.RequestCancelRunning(context.Background(), id, client.ID); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			task, err := rtStore.GetTask(context.Background(), id)
			if err != nil || task.State != domain.TaskCancelled {
				t.Fatalf("during cancel task=%v err=%v", task.State, err)
			}
			if spy.calls.Load() != 1 || other.calls.Load() != 0 {
				t.Fatalf("during cancellation routes selected=%d other=%d", spy.calls.Load(), other.calls.Load())
			}
			if _, err := rtStore.GetResultByTaskID(context.Background(), id); !errors.Is(err, postgres.ErrNotFound) {
				t.Fatalf("during cancellation persisted result: %v", err)
			}
		})
	}
}

// These selectors keep the test's route setup explicit while reusing the dual
// route factory; no production routing is hidden in the fixture.
func alphaFor(capability string, selected, other *h02RouteSpy) *h02RouteSpy {
	if capability == "capability-alpha" {
		return selected
	}
	return other
}
func betaFor(capability string, selected, other *h02RouteSpy) *h02RouteSpy {
	if capability == "capability-beta" {
		return selected
	}
	return other
}

func TestH02TransientRetryPreservesCapabilityAndBackend(t *testing.T) {
	for _, tc := range []struct {
		name, capability, code string
		class                  domain.ErrorClass
	}{
		{name: "alpha-safe", capability: "capability-alpha", class: domain.ErrorTransientSafe, code: "retryable"},
		{name: "alpha-unsent", capability: "capability-alpha", class: domain.ErrorTransientUnsent, code: "connect_failed"},
		{name: "beta-safe", capability: "capability-beta", class: domain.ErrorTransientSafe, code: "retryable"},
		{name: "beta-unsent", capability: "capability-beta", class: domain.ErrorTransientUnsent, code: "connect_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTasks(t)
			cfg, alpha, beta := h02WorkerConfig()
			selected := alpha
			features := []string(nil)
			rc := domain.ResponseContract{Kind: domain.ResponseText}
			if tc.capability == "capability-beta" {
				selected = beta
				features = []string{"structured_output"}
				rc = domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: h02Schema(t)}
			}
			selected.transient.Store(1)
			selected.errorClass = tc.class
			selected.errorCode = tc.code
			w := h02Worker(t, cfg, alpha, beta)
			client := mustClient(t)
			id, p := h02RouteTask(t, cfg, w, client, tc.capability, features, rc, 2)
			claimed := h02Claim(t, w, id)
			if err := w.execute(context.Background(), claimed); err != nil {
				t.Fatal(err)
			}
			requeued, err := rtStore.GetTask(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if requeued.State != domain.TaskQueued || requeued.Capability != tc.capability {
				t.Fatalf("requeued=%+v", requeued)
			}
			if _, err := adminDB.ExecContext(context.Background(), `UPDATE tasks SET retry_available_at = now() WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
			claimed = h02Claim(t, w, id)
			if err := w.execute(context.Background(), claimed); err != nil {
				t.Fatal(err)
			}
			if task, _ := rtStore.GetTask(context.Background(), id); task.State != domain.TaskSucceeded {
				t.Fatalf("retry state=%s", task.State)
			}
			if selected.calls.Load() != 2 || selected.marker == "" || p.AllowedCapabilities[0] != tc.capability {
				t.Fatalf("route retry calls=%d policy=%v", selected.calls.Load(), p.AllowedCapabilities)
			}
			if tc.capability == "capability-beta" && alpha.calls.Load() != 0 || tc.capability == "capability-alpha" && beta.calls.Load() != 0 {
				t.Fatalf("retry crossed route alpha=%d beta=%d", alpha.calls.Load(), beta.calls.Load())
			}
			result, err := rtStore.GetResultByTaskID(context.Background(), id)
			if err != nil || result.Content != selected.marker {
				t.Fatalf("retry result=%+v err=%v want marker %q", result, err, selected.marker)
			}
		})
	}
}

func TestH02IndeterminateRecoveryDoesNotFallbackAcrossRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, capability string
	}{
		{name: "alpha", capability: "capability-alpha"},
		{name: "beta", capability: "capability-beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTasks(t)
			cfg, alpha, beta := h02WorkerConfig()
			selected, other := alpha, beta
			features := []string(nil)
			rc := domain.ResponseContract{Kind: domain.ResponseText}
			if tc.capability == "capability-beta" {
				selected, other = beta, alpha
				features = []string{"structured_output"}
				rc = domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: h02Schema(t)}
			}
			selected.errorClass, selected.errorCode = domain.ErrorIndeterminate, "ambiguous"
			selected.transient.Store(1)
			w := h02Worker(t, cfg, alpha, beta)
			client := mustClient(t)
			id, _ := h02RouteTask(t, cfg, w, client, tc.capability, features, rc, 2)
			if err := w.execute(context.Background(), h02Claim(t, w, id)); err != nil {
				t.Fatal(err)
			}
			task, err := rtStore.GetTask(context.Background(), id)
			if err != nil || task.State != domain.TaskRunning {
				t.Fatalf("indeterminate task=%s err=%v", task.State, err)
			}
			if selected.calls.Load() != 1 || other.calls.Load() != 0 {
				t.Fatalf("fallback calls selected=%d other=%d", selected.calls.Load(), other.calls.Load())
			}
			items, err := rtStore.ListUnresolvedRecovery(context.Background())
			if err != nil || len(items) != 1 || items[0].Task.Capability != tc.capability || items[0].ErrorClass != string(domain.ErrorIndeterminate) || items[0].SafeToRequeue {
				t.Fatalf("indeterminate recovery=%+v err=%v", items, err)
			}
		})
	}
}

func TestH02GlobalSerializationAcrossRoutes(t *testing.T) {
	clearTasks(t)
	cfg, alpha, beta := h02WorkerConfig()
	alpha.started, alpha.release = make(chan struct{}), make(chan struct{})
	beta.started, beta.release = make(chan struct{}), make(chan struct{})
	w := h02Worker(t, cfg, alpha, beta)
	client := mustClient(t)
	aID, _ := h02RouteTask(t, cfg, w, client, "capability-alpha", nil, domain.ResponseContract{Kind: domain.ResponseText}, 1)
	bID, _ := h02RouteTask(t, cfg, w, client, "capability-beta", []string{"structured_output"}, domain.ResponseContract{Kind: domain.ResponseJSONSchema, Schema: h02Schema(t)}, 1)
	claimedA := h02Claim(t, w, aID)
	claimedB := h02Claim(t, w, bID)
	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() { doneA <- w.execute(context.Background(), claimedA) }()
	<-alpha.started
	go func() { doneB <- w.execute(context.Background(), claimedB) }()
	select {
	case <-beta.started:
		t.Fatal("beta inference overlapped alpha")
	default:
	}
	close(alpha.release)
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	<-beta.started
	close(beta.release)
	if err := <-doneB; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{aID, bID} {
		if task, err := rtStore.GetTask(context.Background(), id); err != nil || task.State != domain.TaskSucceeded {
			t.Fatalf("task=%s state=%s err=%v", id, task.State, err)
		}
	}
	if alpha.max.Load() != 1 || beta.max.Load() != 1 {
		t.Fatalf("route max active alpha=%d beta=%d", alpha.max.Load(), beta.max.Load())
	}
	if alpha.shared == nil || alpha.shared.max.Load() != 1 {
		t.Fatalf("shared max active=%v, want 1", alpha.shared)
	}
}

func TestH02RouteSpyObservesContextCancellation(t *testing.T) {
	observed := make(chan struct{})
	release := make(chan struct{})
	spy := &h02RouteSpy{marker: `{"route":"cancel"}`, release: release, cancelObserved: observed}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := spy.Infer(ctx, &backend.Request{}); done <- err }()
	cancel()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("route spy did not observe context cancellation")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Infer error=%v, want context.Canceled", err)
	}
}
