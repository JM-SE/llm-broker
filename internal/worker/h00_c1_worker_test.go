package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
	"github.com/JM-SE/llm-broker/internal/testsupport"
)

func h00C1WorkerProvisioner(t *testing.T) *postgres.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_PROVISIONER_URL")
	if url == "" {
		t.Fatal("H00 C1 worker matrix requires TEST_DATABASE_PROVISIONER_URL")
	}
	store, err := postgres.Open(context.Background(), url, postgres.Options{MaxConns: 4, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open H00 C1 worker provisioner: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

type h00C1WorkerSequenceAdapter struct {
	calls       atomic.Int32
	transient   bool
	started     chan struct{}
	startOnce   sync.Once
	releaseCall <-chan struct{}
}

func (a *h00C1WorkerSequenceAdapter) Type() string { return "h00-c1-sequence" }

func (a *h00C1WorkerSequenceAdapter) Infer(ctx context.Context, _ *backend.Request) (*backend.Response, error) {
	call := a.calls.Add(1)
	if a.started != nil {
		a.startOnce.Do(func() { close(a.started) })
	}
	if a.releaseCall != nil {
		select {
		case <-a.releaseCall:
		case <-ctx.Done():
			return nil, backend.NewError(domain.ErrorTransientUnsent, "h00_c1_cancelled", "test call cancelled")
		}
	}
	if a.transient && call == 1 {
		return nil, backend.NewError(domain.ErrorTransientSafe, "h00_c1_retry", "test retry")
	}
	return &backend.Response{
		Content:      "h00 c1 response",
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}, nil
}

func h00C1WorkerTaskFixture(t *testing.T, provisioner *postgres.Store, maxAttempts int) (postgres.Client, postgres.WorkloadPolicy, postgres.TaskWithPayload) {
	t.Helper()
	clearTasks(t)
	client := mustClient(t)
	workload := mustPolicy(t, "h00-c1-worker", maxAttempts, []int{1})
	if err := provisioner.GrantWorkloadBySlug(context.Background(), client.ID, workload.Name); err != nil {
		t.Fatalf("grant H00 C1 worker workload: %v", err)
	}
	payload := payloadFor(t, workload.Name, "instruct",
		[]domain.Message{{Role: domain.RoleUser, Content: "h00 c1 worker"}}, textResponse)
	taskID := mustEnqueue(t, client.ID, workload.ID, uniqueName("h00-c1-worker-key"), payload)
	claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim H00 C1 worker task: %v", err)
	}
	if claimed.ID != taskID || claimed.WorkloadSlug != workload.Name {
		t.Fatalf("H00 C1 worker claim = %+v, want task %s/slug %s", claimed, taskID, workload.Name)
	}
	return client, workload, claimed
}

func h00C1WorkerClaimEligible(t *testing.T, w *Worker) postgres.TaskWithPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := w.leader.TryAcquire(ctx)
	if err != nil || !ok {
		t.Fatalf("acquire H00 C1 worker claim leadership: ok=%v err=%v", ok, err)
	}
	defer w.leader.Release()
	result, err := w.sched.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("claim H00 C1 eligible task: %v", err)
	}
	if result.NoTask {
		t.Fatal("H00 C1 eligible claim returned no task")
	}
	return result.Task
}

type h00C1WorkerRevocation struct {
	name       string
	key        func(*testing.T, postgres.Client, postgres.WorkloadPolicy) int64
	mutate     func(context.Context, *postgres.Store, postgres.Client, postgres.WorkloadPolicy) error
	wantGrant  bool
	wantClient bool
	wantCaps   []string
}

func h00C1WorkerRevocations() []h00C1WorkerRevocation {
	return []h00C1WorkerRevocation{
		{
			name: "client-disable",
			key: func(t *testing.T, client postgres.Client, _ postgres.WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, provisioner *postgres.Store, client postgres.Client, _ postgres.WorkloadPolicy) error {
				return provisioner.SetClientEnabled(ctx, client.ID, false)
			},
			wantClient: false,
			wantGrant:  true,
		},
		{
			name: "ungrant",
			key: func(t *testing.T, client postgres.Client, _ postgres.WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, provisioner *postgres.Store, client postgres.Client, workload postgres.WorkloadPolicy) error {
				return provisioner.RevokeWorkloadBySlug(ctx, client.ID, workload.Name)
			},
			wantClient: true,
			wantGrant:  false,
		},
		{
			name: "allowlist-removal",
			key: func(t *testing.T, _ postgres.Client, workload postgres.WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.PolicyLockKey(workload.Name)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, provisioner *postgres.Store, _ postgres.Client, workload postgres.WorkloadPolicy) error {
				workload.AllowedCapabilities = []string{}
				_, err := testsupport.ApplyPolicy(ctx, provisioner, workload)
				return err
			},
			wantClient: true,
			wantGrant:  true,
			wantCaps:   []string{},
		},
	}
}

func TestH00C1QueuedRevocationRemainsClaimableAndRetryCarriesSlug(t *testing.T) {
	provisioner := h00C1WorkerProvisioner(t)
	for _, revocation := range h00C1WorkerRevocations() {
		revocation := revocation
		t.Run(revocation.name, func(t *testing.T) {
			client, workload, claimed := h00C1WorkerTaskFixture(t, provisioner, 2)
			adapter := &h00C1WorkerSequenceAdapter{transient: true}
			w, _, _ := newWorkerEnv(t, "none", 0)
			w.registry = backend.NewRegistry()
			w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
				return adapter, nil
			})
			if err := w.execute(context.Background(), claimed); err != nil {
				t.Fatalf("initial H00 C1 worker attempt: %v", err)
			}
			if got := adapter.calls.Load(); got != 1 {
				t.Fatalf("initial H00 C1 adapter calls = %d, want 1", got)
			}
			first, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
			if err != nil || first.State != postgres.AttemptPhaseFailed || first.ErrorCode == nil || *first.ErrorCode != "h00_c1_retry" {
				t.Fatalf("initial retry attempt = %+v, err=%v", first, err)
			}
			queued, err := rtStore.GetTask(context.Background(), claimed.ID)
			if err != nil || queued.State != domain.TaskQueued {
				t.Fatalf("task after safe retry = %+v, err=%v; want queued", queued, err)
			}

			if err := revocation.mutate(context.Background(), provisioner, client, workload); err != nil {
				t.Fatalf("apply queued %s revocation: %v", revocation.name, err)
			}
			queuedID := mustEnqueue(t, client.ID, workload.ID, uniqueName("h00-c1-post-revocation-key"), payloadFor(t,
				workload.Name, "instruct", []domain.Message{{Role: domain.RoleUser, Content: "queued after revocation"}}, textResponse))
			queuedClaim := h00C1WorkerClaimEligible(t, w)
			if queuedClaim.ID != queuedID || queuedClaim.WorkloadSlug != workload.Name {
				t.Fatalf("queued %s claim = %+v, want task %s/immutable slug %s", revocation.name, queuedClaim, queuedID, workload.Name)
			}
			if err := w.execute(context.Background(), queuedClaim); err != nil {
				t.Fatalf("terminalize queued %s task: %v", revocation.name, err)
			}
			if got := adapter.calls.Load(); got != 1 {
				t.Fatalf("queued %s revocation adapter calls = %d, want no call after mutation", revocation.name, got)
			}
			queuedFinal, err := rtStore.GetTask(context.Background(), queuedID)
			if err != nil || queuedFinal.State != domain.TaskFailed {
				t.Fatalf("queued %s final task = %+v, err=%v; want failed", revocation.name, queuedFinal, err)
			}
			queuedAttempt, err := rtStore.GetLastAttempt(context.Background(), queuedID)
			if err != nil || queuedAttempt.State != postgres.AttemptPhaseFailed || queuedAttempt.ErrorClass == nil ||
				*queuedAttempt.ErrorClass != string(domain.ErrorPolicyRejected) || queuedAttempt.ErrorCode == nil ||
				*queuedAttempt.ErrorCode != "authorization_revoked" {
				t.Fatalf("queued %s final attempt = %+v, err=%v", revocation.name, queuedAttempt, err)
			}
			// The worker's retry backoff is a durable queue field. Move that
			// fixture field to now with the owner role rather than waiting.
			if _, err := adminDB.ExecContext(context.Background(),
				`UPDATE tasks SET retry_available_at = now() WHERE id = $1`, claimed.ID); err != nil {
				t.Fatalf("make retry claimable: %v", err)
			}
			retried := h00C1WorkerClaimEligible(t, w)
			if retried.ID != claimed.ID || retried.WorkloadSlug != workload.Name || retried.Attempts != 2 {
				t.Fatalf("retry claim = %+v, want same task/immutable slug/attempt 2", retried)
			}
			if err := w.execute(context.Background(), retried); err != nil {
				t.Fatalf("revoked retry execute: %v", err)
			}
			if got := adapter.calls.Load(); got != 1 {
				t.Fatalf("revoked retry adapter calls = %d, want exactly initial one", got)
			}
			finalTask, err := rtStore.GetTask(context.Background(), claimed.ID)
			if err != nil || finalTask.State != domain.TaskFailed || finalTask.Attempts != 2 {
				t.Fatalf("final revoked retry task = %+v, err=%v; want failed/attempts 2", finalTask, err)
			}
			finalAttempt, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
			if err != nil || finalAttempt.State != postgres.AttemptPhaseFailed || finalAttempt.ErrorClass == nil ||
				*finalAttempt.ErrorClass != string(domain.ErrorPolicyRejected) || finalAttempt.ErrorCode == nil ||
				*finalAttempt.ErrorCode != "authorization_revoked" {
				t.Fatalf("final revoked retry attempt = %+v, err=%v", finalAttempt, err)
			}
			if _, err := rtStore.GetResultByTaskID(context.Background(), claimed.ID); !errors.Is(err, postgres.ErrNotFound) {
				t.Fatalf("revoked retry produced a result: %v", err)
			}
			currentClient, err := provisioner.GetClientByID(context.Background(), client.ID)
			if err != nil || currentClient.Enabled != revocation.wantClient {
				t.Fatalf("final client after %s = %+v, err=%v", revocation.name, currentClient, err)
			}
			granted, err := provisioner.ClientHasWorkload(context.Background(), client.ID, workload.ID)
			if err != nil || granted != revocation.wantGrant {
				t.Fatalf("final grant after %s = %v, err=%v; want %v", revocation.name, granted, err, revocation.wantGrant)
			}
			finalPolicy, err := provisioner.GetWorkloadPolicyByID(context.Background(), workload.ID)
			if err != nil {
				t.Fatalf("read final policy after %s: %v", revocation.name, err)
			}
			if revocation.wantCaps != nil && fmt.Sprint(finalPolicy.AllowedCapabilities) != fmt.Sprint(revocation.wantCaps) {
				t.Fatalf("final capabilities after %s = %v, want %v", revocation.name, finalPolicy.AllowedCapabilities, revocation.wantCaps)
			}
		})
	}
}

type h00C1WorkerBarrier struct {
	tx       *sql.Tx
	released bool
}

func newH00C1WorkerBarrier(t *testing.T, key int64) *h00C1WorkerBarrier {
	t.Helper()
	tx, err := adminDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin H00 C1 worker barrier: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `SET LOCAL lock_timeout = '3s'`); err != nil {
		t.Fatalf("bound H00 C1 worker barrier: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatalf("acquire H00 C1 worker barrier: %v", err)
	}
	barrier := &h00C1WorkerBarrier{tx: tx}
	t.Cleanup(func() {
		if !barrier.released {
			_ = barrier.tx.Rollback()
		}
	})
	return barrier
}

func (b *h00C1WorkerBarrier) release(t *testing.T) {
	t.Helper()
	if b.released {
		return
	}
	if err := b.tx.Commit(); err != nil {
		t.Fatalf("release H00 C1 worker barrier: %v", err)
	}
	b.released = true
}

func waitForH00C1WorkerWaiter(t *testing.T, key int64, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := adminDB.QueryRowContext(context.Background(), `
			SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND objsubid = 1
			  AND classid::bigint * 4294967296 + objid::bigint = $1
			  AND NOT granted`, key).Scan(&waiting); err != nil {
			t.Fatalf("inspect H00 C1 worker waiter: %v", err)
		}
		if waiting == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("H00 C1 worker operation did not wait on advisory key %d", key)
}

func TestH00C1IntegratedWorkerRevocationBothWinnerAdapterMatrix(t *testing.T) {
	provisioner := h00C1WorkerProvisioner(t)
	for _, revocation := range h00C1WorkerRevocations() {
		revocation := revocation
		for _, workerFirst := range []bool{true, false} {
			name := "mutation-wins"
			if workerFirst {
				name = "authorized-first"
			}
			t.Run(revocation.name+"/"+name, func(t *testing.T) {
				client, workload, claimed := h00C1WorkerTaskFixture(t, provisioner, 2)
				started := make(chan struct{})
				releaseInference := make(chan struct{})
				adapter := &h00C1WorkerSequenceAdapter{started: started, releaseCall: releaseInference}
				w, _, _ := newWorkerEnv(t, "none", 0)
				w.registry = backend.NewRegistry()
				w.registry.Register("fake", func(config.BackendConfig) (backend.Adapter, error) {
					return adapter, nil
				})

				key := revocation.key(t, client, workload)
				barrier := newH00C1WorkerBarrier(t, key)
				workerDone := make(chan error, 1)
				mutationDone := make(chan error, 1)
				worker := func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					workerDone <- w.execute(ctx, claimed)
				}
				mutate := func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					mutationDone <- revocation.mutate(ctx, provisioner, client, workload)
				}

				if workerFirst {
					go worker()
					waitForH00C1WorkerWaiter(t, key, 1)
					go mutate()
					waitForH00C1WorkerWaiter(t, key, 2)
				} else {
					go mutate()
					waitForH00C1WorkerWaiter(t, key, 1)
					go worker()
					waitForH00C1WorkerWaiter(t, key, 2)
				}
				barrier.release(t)

				if workerFirst {
					select {
					case <-started:
					case err := <-workerDone:
						t.Fatalf("authorized-first worker returned before adapter boundary: %v", err)
					case <-time.After(5 * time.Second):
						t.Fatal("authorized-first worker never reached adapter")
					}
					select {
					case err := <-mutationDone:
						if err != nil {
							t.Fatalf("authorized-first later mutation: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("later mutation waited for inference; authorization transaction spanned adapter call")
					}
					close(releaseInference)
					select {
					case err := <-workerDone:
						if err != nil {
							t.Fatalf("authorized-first worker = %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("authorized-first worker did not finish after adapter release")
					}
				} else {
					select {
					case err := <-mutationDone:
						if err != nil {
							t.Fatalf("mutation-first mutation: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("mutation-first mutation did not finish")
					}
					select {
					case err := <-workerDone:
						if err != nil {
							t.Fatalf("mutation-first worker operational error: %v", err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("mutation-first worker did not finish")
					}
				}

				if workerFirst {
					if got := adapter.calls.Load(); got != 1 {
						t.Fatalf("authorized-first adapter calls = %d, want exactly 1", got)
					}
					task, err := rtStore.GetTask(context.Background(), claimed.ID)
					if err != nil || task.State != domain.TaskSucceeded {
						t.Fatalf("authorized-first final task = %+v, err=%v; want succeeded", task, err)
					}
					attempt, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
					if err != nil || attempt.State != postgres.AttemptPhaseSucceeded {
						t.Fatalf("authorized-first final attempt = %+v, err=%v", attempt, err)
					}
					if _, err := rtStore.GetResultByTaskID(context.Background(), claimed.ID); err != nil {
						t.Fatalf("authorized-first result: %v", err)
					}
				} else {
					if got := adapter.calls.Load(); got != 0 {
						t.Fatalf("mutation-first adapter calls = %d, want zero", got)
					}
					task, err := rtStore.GetTask(context.Background(), claimed.ID)
					if err != nil || task.State != domain.TaskFailed {
						t.Fatalf("mutation-first final task = %+v, err=%v; want failed", task, err)
					}
					attempt, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
					if err != nil || attempt.State != postgres.AttemptPhaseFailed || attempt.ErrorClass == nil ||
						*attempt.ErrorClass != string(domain.ErrorPolicyRejected) || attempt.ErrorCode == nil ||
						*attempt.ErrorCode != "authorization_revoked" {
						t.Fatalf("mutation-first final attempt = %+v, err=%v", attempt, err)
					}
					if _, err := rtStore.GetResultByTaskID(context.Background(), claimed.ID); !errors.Is(err, postgres.ErrNotFound) {
						t.Fatalf("mutation-first produced result: %v", err)
					}
				}
				currentClient, err := provisioner.GetClientByID(context.Background(), client.ID)
				if err != nil || currentClient.Enabled != revocation.wantClient {
					t.Fatalf("final client after %s = %+v, err=%v", revocation.name, currentClient, err)
				}
				granted, err := provisioner.ClientHasWorkload(context.Background(), client.ID, workload.ID)
				if err != nil || granted != revocation.wantGrant {
					t.Fatalf("final grant after %s = %v, err=%v", revocation.name, granted, err)
				}
				if revocation.name == "allowlist-removal" {
					finalPolicy, err := provisioner.GetWorkloadPolicyByID(context.Background(), workload.ID)
					if err != nil || len(finalPolicy.AllowedCapabilities) != 0 {
						t.Fatalf("final allowlist after mutation = %+v, err=%v", finalPolicy.AllowedCapabilities, err)
					}
				}
			})
		}
	}
}
