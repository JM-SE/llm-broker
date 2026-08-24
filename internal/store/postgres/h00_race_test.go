package postgres

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// h00LockBarrier holds the lock that the two operations under test must
// linearize on. The operations are queued behind the same PostgreSQL lock
// before the barrier is released, so the order in which they were queued is
// the only ordering decision left to PostgreSQL.
type h00LockBarrier struct {
	tx       pgx.Tx
	released bool
}

func newH00LockBarrier(t *testing.T, keys ...int64) *h00LockBarrier {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := adminStore.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin H00 lock barrier: %v", err)
	}
	barrier := &h00LockBarrier{tx: tx}
	t.Cleanup(func() {
		if !barrier.released {
			_ = barrier.tx.Rollback(context.Background())
		}
	})
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		t.Fatalf("bound H00 barrier lock timeout: %v", err)
	}
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			t.Fatalf("acquire H00 barrier lock %d: %v", key, err)
		}
	}
	return barrier
}

func (b *h00LockBarrier) release(t *testing.T) {
	t.Helper()
	if b.released {
		return
	}
	if err := b.tx.Commit(context.Background()); err != nil {
		t.Fatalf("release H00 lock barrier: %v", err)
	}
	b.released = true
}

func waitForH00AdvisoryWaiters(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiters int
		if err := adminStore.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiters); err != nil {
			t.Fatalf("inspect H00 advisory waiters: %v", err)
		}
		if waiters >= want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %d H00 advisory lock waiters", want)
}

func runH00LockRace(t *testing.T, barrierKey int64, first, second func(context.Context) error) (error, error) {
	t.Helper()
	barrier := newH00LockBarrier(t, barrierKey)

	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSecond()

	firstDone := make(chan error, 1)
	go func() { firstDone <- first(firstCtx) }()
	waitForH00AdvisoryWaiters(t, 1)

	secondDone := make(chan error, 1)
	go func() { secondDone <- second(secondCtx) }()
	waitForH00AdvisoryWaiters(t, 2)

	barrier.release(t)
	return waitForH00RaceResult(t, "first", firstDone),
		waitForH00RaceResult(t, "second", secondDone)
}

func waitForH00RaceResult(t *testing.T, name string, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("H00 %s race operation did not finish", name)
		return context.DeadlineExceeded
	}
}

type h00RaceMutation struct {
	name              string
	barrierKey        func(t *testing.T, client Client, workload WorkloadPolicy) int64
	mutate            func(context.Context, Client, WorkloadPolicy) error
	submissionError   error
	preflightSucceeds bool
}

func h00RaceMutations() []h00RaceMutation {
	return []h00RaceMutation{
		{
			name: "revoke",
			barrierKey: func(t *testing.T, client Client, workload WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, client Client, workload WorkloadPolicy) error {
				return adminStore.RevokeWorkloadBySlug(ctx, client.ID, workload.Name)
			},
		},
		{
			name: "disable",
			barrierKey: func(t *testing.T, client Client, _ WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, client Client, _ WorkloadPolicy) error {
				return adminStore.SetClientEnabled(ctx, client.ID, false)
			},
			submissionError: ErrSubmissionCredentialInvalid,
		},
		{
			name: "credential-rotation",
			barrierKey: func(t *testing.T, client Client, _ WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, client Client, _ WorkloadPolicy) error {
				identifier := fmt.Sprintf("rt%010d", fixtureSeq.Add(1))
				_, err := adminStore.RotateClientCredential(ctx, client.ID, identifier, testHash(0xa4))
				return err
			},
			submissionError:   ErrSubmissionCredentialInvalid,
			preflightSucceeds: true,
		},
		{
			name: "ungrant",
			barrierKey: func(t *testing.T, client Client, _ WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.ClientLockKey(client.ID)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, client Client, workload WorkloadPolicy) error {
				return adminStore.RevokeWorkloadBySlug(ctx, client.ID, workload.Name)
			},
		},
		{
			name: "policy-allowlist",
			barrierKey: func(t *testing.T, _ Client, workload WorkloadPolicy) int64 {
				t.Helper()
				key, err := policy.PolicyLockKey(workload.Name)
				if err != nil {
					t.Fatal(err)
				}
				return key
			},
			mutate: func(ctx context.Context, _ Client, workload WorkloadPolicy) error {
				_, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{
					h00PolicyProjection(workload, nil),
				})
				return err
			},
		},
	}
}

func h00PolicyProjection(p WorkloadPolicy, allowedCapabilities []string) policy.Workload {
	var digest [32]byte
	copy(digest[:], p.TopologyDigest)
	return policy.Workload{
		Name:                     p.Name,
		PriorityClass:            p.PriorityClass,
		MaxActiveTasks:           p.MaxActiveTasks,
		MaxAttempts:              p.MaxAttempts,
		RetryDelaysSeconds:       append([]int(nil), p.RetryDelaysSeconds...),
		MaxOutputTokens:          p.MaxOutputTokens,
		AllowedCapabilities:      append([]string(nil), allowedCapabilities...),
		AllowedGeneration:        append([]byte(nil), p.AllowedGeneration...),
		RequiredFeatures:         append([]string{}, p.RequiredFeatures...),
		WorkloadTimeoutSeconds:   p.WorkloadTimeoutSeconds,
		PayloadRetentionSeconds:  p.PayloadRetentionSeconds,
		ResultRetentionSeconds:   p.ResultRetentionSeconds,
		MetadataRetentionSeconds: p.MetadataRetentionSeconds,
		TopologyDigest:           digest,
	}
}

func h00RaceFixture(t *testing.T) (Client, WorkloadPolicy, SubmissionParams) {
	t.Helper()
	client := mustCreateClient(t, adminStore, "h00-race-client")
	workload := h00Policy(t, adminStore, uniqueName("h00-race-workload"), testHash(0x91), 10)
	if err := adminStore.GrantWorkloadBySlug(context.Background(), client.ID, workload.Name); err != nil {
		t.Fatalf("grant H00 race workload: %v", err)
	}
	return client, workload, h00SubmitParams(t, client, workload,
		uniqueName("h00-race-key"), testHash(0x92))
}

func TestH00SubmissionAuthorizationLockLinearization(t *testing.T) {
	for _, mutation := range h00RaceMutations() {
		mutation := mutation
		t.Run(mutation.name+"/submission-wins", func(t *testing.T) {
			runH00SubmissionRace(t, mutation, true)
		})
		t.Run(mutation.name+"/mutation-wins", func(t *testing.T) {
			runH00SubmissionRace(t, mutation, false)
		})
	}
}

func TestH00GrantCreationSubmissionLockLinearization(t *testing.T) {
	for _, submissionFirst := range []bool{true, false} {
		name := "grant-wins"
		if submissionFirst {
			name = "submission-wins"
		}
		t.Run(name, func(t *testing.T) {
			client := mustCreateClient(t, adminStore, "h00-grant-race-client")
			workload := h00Policy(t, adminStore, uniqueName("h00-grant-race-workload"), testHash(0x94), 10)
			params := h00SubmitParams(t, client, workload,
				uniqueName("h00-grant-race-key"), testHash(0x95))

			var submitted SubmissionResult
			submit := func(ctx context.Context) error {
				var err error
				submitted, err = rtStore.SubmitTask(ctx, params)
				return err
			}
			grant := func(ctx context.Context) error {
				return provisionerStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name)
			}
			clientKey, err := policy.ClientLockKey(client.ID)
			if err != nil {
				t.Fatal(err)
			}

			var firstErr, secondErr error
			if submissionFirst {
				firstErr, secondErr = runH00LockRace(t, clientKey, submit, grant)
				if !errors.Is(firstErr, ErrSubmissionForbidden) || secondErr != nil {
					t.Fatalf("submission-wins grant race errors: submit=%v grant=%v", firstErr, secondErr)
				}
				if submitted.Created {
					t.Fatalf("denied submission unexpectedly created a task: %+v", submitted)
				}
			} else {
				firstErr, secondErr = runH00LockRace(t, clientKey, grant, submit)
				if firstErr != nil || secondErr != nil || !submitted.Created {
					t.Fatalf("grant-wins race: grant=%v submit=%v result=%+v", firstErr, secondErr, submitted)
				}
			}

			granted, err := provisionerStore.ClientHasWorkload(context.Background(), client.ID, workload.ID)
			if err != nil {
				t.Fatalf("read grant after race: %v", err)
			}
			if !granted {
				t.Fatal("grant was not committed")
			}
			var rows int
			if err := adminStore.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM tasks WHERE client_id=$1 AND idempotency_key=$2`,
				client.ID, params.IdempotencyKey).Scan(&rows); err != nil {
				t.Fatalf("count grant race task rows: %v", err)
			}
			wantRows := 0
			if !submissionFirst {
				wantRows = 1
			}
			if rows != wantRows {
				t.Fatalf("grant race task rows = %d, want %d", rows, wantRows)
			}
		})
	}
}

func runH00SubmissionRace(t *testing.T, mutation h00RaceMutation, submissionFirst bool) {
	t.Helper()
	client, workload, params := h00RaceFixture(t)
	t.Cleanup(func() {
		_, _ = adminStore.pool.Exec(context.Background(),
			`DELETE FROM tasks WHERE client_id = $1`, client.ID)
	})

	var submitted SubmissionResult
	submit := func(ctx context.Context) error {
		var err error
		submitted, err = rtStore.SubmitTask(ctx, params)
		return err
	}
	mutate := func(ctx context.Context) error { return mutation.mutate(ctx, client, workload) }
	barrierKey := mutation.barrierKey(t, client, workload)

	var firstErr, secondErr error
	if submissionFirst {
		firstErr, secondErr = runH00LockRace(t, barrierKey, submit, mutate)
		if firstErr != nil || secondErr != nil {
			t.Fatalf("submission-wins race errors: submit=%v mutate=%v", firstErr, secondErr)
		}
		if !submitted.Created {
			t.Fatalf("submission-wins result = %+v, want created task", submitted)
		}
	} else {
		firstErr, secondErr = runH00LockRace(t, barrierKey, mutate, submit)
		if firstErr != nil {
			t.Fatalf("mutation-wins mutation error: %v", firstErr)
		}
		wantErr := mutation.submissionError
		if wantErr == nil {
			wantErr = ErrSubmissionForbidden
		}
		if !errors.Is(secondErr, wantErr) {
			t.Fatalf("mutation-wins submission error = %v, want %v", secondErr, wantErr)
		}
		if submitted.Created {
			t.Fatalf("mutation-wins unexpectedly created task: %+v", submitted)
		}
	}

	var rows int
	if err := adminStore.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tasks WHERE client_id=$1 AND idempotency_key=$2`,
		client.ID, params.IdempotencyKey).Scan(&rows); err != nil {
		t.Fatalf("count H00 race task rows: %v", err)
	}
	wantRows := 0
	if submissionFirst {
		wantRows = 1
	}
	if rows != wantRows {
		t.Fatalf("H00 race task rows = %d, want %d", rows, wantRows)
	}
}

func h00PreflightFixture(t *testing.T) (Client, WorkloadPolicy, ClaimedTask) {
	t.Helper()
	clearQueuedTasks(t)
	client, workload, _ := h00RaceFixture(t)
	created := mustCreateTask(t, adminStore, client.ID, workload.ID,
		uniqueName("h00-preflight-race-key"), testHash(0x93))
	claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim H00 preflight race task: %v", err)
	}
	if claimed.ID != created.TaskID {
		t.Fatalf("claimed H00 preflight race task = %s, want %s", claimed.ID, created.TaskID)
	}
	return client, workload, ClaimedTask{TaskWithPayload: claimed}
}

func TestH00PreflightAuthorizationLockLinearization(t *testing.T) {
	for _, mutation := range h00RaceMutations() {
		mutation := mutation
		t.Run(mutation.name+"/preflight-wins", func(t *testing.T) {
			runH00PreflightRace(t, mutation, true)
		})
		t.Run(mutation.name+"/mutation-wins", func(t *testing.T) {
			runH00PreflightRace(t, mutation, false)
		})
	}
}

func runH00PreflightRace(t *testing.T, mutation h00RaceMutation, preflightFirst bool) {
	t.Helper()
	client, workload, claimed := h00PreflightFixture(t)
	t.Cleanup(func() {
		_, _ = adminStore.pool.Exec(context.Background(),
			`DELETE FROM tasks WHERE client_id = $1`, client.ID)
	})

	preflight := func(ctx context.Context) error {
		_, err := rtStore.PreflightTask(ctx, PreflightParams{
			TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
			WorkloadID: workload.ID, WorkloadSlug: workload.Name, Capability: "instruct",
			LocalMembership: true, ConfiguredCapability: true,
			LocalTopologyDigest: h00Digest(0x91),
		})
		return err
	}
	mutate := func(ctx context.Context) error { return mutation.mutate(ctx, client, workload) }
	barrierKey := mutation.barrierKey(t, client, workload)
	var firstErr, secondErr error
	if preflightFirst {
		firstErr, secondErr = runH00LockRace(t, barrierKey, preflight, mutate)
		if firstErr != nil || secondErr != nil {
			t.Fatalf("preflight-wins race errors: preflight=%v mutate=%v", firstErr, secondErr)
		}
	} else {
		firstErr, secondErr = runH00LockRace(t, barrierKey, mutate, preflight)
		if firstErr != nil {
			t.Fatalf("mutation-wins mutation error: %v", firstErr)
		}
		if !mutation.preflightSucceeds {
			if !errors.Is(secondErr, ErrPreflightPolicyRejected) {
				t.Fatalf("mutation-wins preflight error = %v, want policy rejection", secondErr)
			}
		} else if secondErr != nil {
			t.Fatalf("mutation-wins preflight error = %v, want success", secondErr)
		}
	}

	task, err := rtStore.GetTask(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("read H00 preflight race task: %v", err)
	}
	attempt, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
	if err != nil {
		t.Fatalf("read H00 preflight race attempt: %v", err)
	}
	if preflightFirst {
		if task.State != domain.TaskRunning || attempt.State != "authorized" {
			t.Fatalf("preflight-wins state = task %s/attempt %s, want running/authorized", task.State, attempt.State)
		}
	} else if !mutation.preflightSucceeds {
		if task.State != domain.TaskFailed || attempt.State != "failed" ||
			attempt.ErrorCode == nil || *attempt.ErrorCode != "authorization_revoked" {
			t.Fatalf("mutation-wins state = task %+v/attempt %+v, want failed authorization_revoked", task, attempt)
		}
	} else if task.State != domain.TaskRunning || attempt.State != "authorized" {
		t.Fatalf("mutation-wins non-preemptive state = task %s/attempt %s, want running/authorized", task.State, attempt.State)
	}
}
