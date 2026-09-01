package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/google/uuid"
)

type h00C1StoreMutation struct {
	name          string
	barrierKey    func(*testing.T, Client, WorkloadPolicy) int64
	mutate        func(context.Context, Client, WorkloadPolicy) error
	wantSubmitErr error
	wantTables    []string
}

func h00C1StoreFixture(t *testing.T, maxActive int) (Client, WorkloadPolicy, SubmissionParams) {
	t.Helper()
	client := mustCreateClient(t, adminStore, "h00-c1-client")
	digest := h00Digest(0x41)
	workload := h00Policy(t, adminStore, uniqueName("h00-c1-workload"), digest[:], maxActive)
	if err := provisionerStore.GrantWorkloadBySlug(context.Background(), client.ID, workload.Name); err != nil {
		t.Fatalf("grant H00 C1 fixture workload: %v", err)
	}
	params := h00SubmitParams(t, client, workload, uniqueName("h00-c1-key"), testHash(0x42))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = adminStore.pool.Exec(ctx, `DELETE FROM tasks WHERE client_id = $1 AND workload_id = $2`, client.ID, workload.ID)
		_, _ = adminStore.pool.Exec(ctx, `DELETE FROM client_workload_grants WHERE client_id = $1 AND workload_id = $2`, client.ID, workload.ID)
		_, _ = adminStore.pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, client.ID)
		_, _ = adminStore.pool.Exec(ctx, `DELETE FROM workload_policies WHERE id = $1`, workload.ID)
	})
	return client, workload, params
}

func h00C1StoreMutations() []h00C1StoreMutation {
	return []h00C1StoreMutation{
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
				return provisionerStore.RevokeWorkloadBySlug(ctx, client.ID, workload.Name)
			},
			wantSubmitErr: ErrSubmissionForbidden,
			wantTables:    []string{"grants"},
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
				return provisionerStore.SetClientEnabled(ctx, client.ID, false)
			},
			wantSubmitErr: ErrSubmissionCredentialInvalid,
			wantTables:    []string{"clients"},
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
				return func() error {
					identifier := fmt.Sprintf("rt%010d", fixtureSeq.Add(1))
					_, err := provisionerStore.RotateClientCredential(ctx, client.ID, identifier, testHash(0xa4))
					return err
				}()
			},
			wantSubmitErr: ErrSubmissionCredentialInvalid,
			wantTables:    []string{"clients"},
		},
		{
			name: "allowlist-removal",
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
					h00PolicyProjection(workload, []string{}),
				})
				return err
			},
			wantSubmitErr: ErrSubmissionForbidden,
			wantTables:    []string{"policies", "capabilities"},
		},
	}
}

func TestH00C1MutationSubmissionBothWinnerExactSnapshots(t *testing.T) {
	for _, mutation := range h00C1StoreMutations() {
		mutation := mutation
		for _, submissionFirst := range []bool{true, false} {
			name := "mutation-wins"
			if submissionFirst {
				name = "submission-wins"
			}
			t.Run(mutation.name+"/"+name, func(t *testing.T) {
				client, workload, params := h00C1StoreFixture(t, 10)
				before := h00C1SnapshotFor(t, client.ID, workload.ID)
				var submitted SubmissionResult
				submit := func(ctx context.Context) error {
					var err error
					submitted, err = rtStore.SubmitTask(ctx, params)
					return err
				}
				mutate := func(ctx context.Context) error {
					return mutation.mutate(ctx, client, workload)
				}
				barrierKey := mutation.barrierKey(t, client, workload)

				if submissionFirst {
					submitErr, mutateErr := runH00LockRace(t, barrierKey, submit, mutate)
					if submitErr != nil || mutateErr != nil {
						t.Fatalf("submission-wins errors: submit=%v mutate=%v", submitErr, mutateErr)
					}
					if !submitted.Created {
						t.Fatalf("submission-wins result = %+v, want one created task", submitted)
					}
				} else {
					mutateErr, submitErr := runH00LockRace(t, barrierKey, mutate, submit)
					if mutateErr != nil {
						t.Fatalf("mutation-wins mutation error: %v", mutateErr)
					}
					if !errors.Is(submitErr, mutation.wantSubmitErr) {
						t.Fatalf("mutation-wins submission error = %v, want %v", submitErr, mutation.wantSubmitErr)
					}
					if submitted.Created {
						t.Fatalf("mutation-wins unexpectedly created task: %+v", submitted)
					}
				}

				after := h00C1SnapshotFor(t, client.ID, workload.ID)
				wantTables := append([]string(nil), mutation.wantTables...)
				if submissionFirst {
					wantTables = append(wantTables, "tasks", "payloads")
				}
				h00C1AssertChangedTables(t, before, after, wantTables...)
				h00C1AssertMutationSubmissionFinalState(t, client, workload, params, submitted, submissionFirst, mutation.name)
			})
		}
	}
}

func h00C1AssertMutationSubmissionFinalState(t *testing.T, client Client, workload WorkloadPolicy, params SubmissionParams, submitted SubmissionResult, submissionFirst bool, mutation string) {
	t.Helper()
	currentClient, err := adminStore.GetClientByID(context.Background(), client.ID)
	if err != nil {
		t.Fatalf("read H00 C1 client: %v", err)
	}
	switch mutation {
	case "disable":
		if currentClient.Enabled {
			t.Fatal("disabled H00 C1 client remained enabled")
		}
	case "credential-rotation":
		if currentClient.CredentialIdentifier == client.CredentialIdentifier ||
			!reflect.DeepEqual(currentClient.CredentialDigest, testHash(0xa4)) {
			t.Fatalf("rotated H00 C1 client = %+v, want a new identifier and exact replacement digest", currentClient)
		}
	default:
		if !currentClient.Enabled || !reflect.DeepEqual(currentClient.CredentialDigest, client.CredentialDigest) {
			t.Fatalf("unrelated H00 C1 client fields changed: before=%+v after=%+v", client, currentClient)
		}
	}

	granted, err := provisionerStore.ClientHasWorkload(context.Background(), client.ID, workload.ID)
	if err != nil {
		t.Fatalf("read H00 C1 final grant: %v", err)
	}
	wantGranted := mutation != "ungrant"
	if granted != wantGranted {
		t.Fatalf("H00 C1 final grant = %v, want %v", granted, wantGranted)
	}
	actualPolicy, err := provisionerStore.GetWorkloadPolicyByID(context.Background(), workload.ID)
	if err != nil {
		t.Fatalf("read H00 C1 final policy: %v", err)
	}
	wantCapabilities := workload.AllowedCapabilities
	if mutation == "allowlist-removal" {
		wantCapabilities = []string{}
	}
	if !reflect.DeepEqual(actualPolicy.AllowedCapabilities, wantCapabilities) {
		t.Fatalf("H00 C1 final capabilities = %v, want %v", actualPolicy.AllowedCapabilities, wantCapabilities)
	}

	rows := h00C1TaskRows(t, client.ID, workload.ID)
	wantTaskRows := 0
	if submissionFirst {
		wantTaskRows = 1
	}
	if len(rows) != wantTaskRows {
		t.Fatalf("H00 C1 final task rows = %d, want %d", len(rows), wantTaskRows)
	}
	wantExecutionRows := 0
	if h00C1Count(t, `SELECT count(*) FROM task_attempts a JOIN tasks t ON t.id = a.task_id WHERE t.client_id = $1 AND t.workload_id = $2`, client.ID, workload.ID) != wantExecutionRows {
		t.Fatalf("H00 C1 attempts exist after submission race")
	}
	if h00C1Count(t, `SELECT count(*) FROM task_results r JOIN tasks t ON t.id = r.task_id WHERE t.client_id = $1 AND t.workload_id = $2`, client.ID, workload.ID) != wantExecutionRows {
		t.Fatalf("H00 C1 results exist after submission race")
	}
	if !submissionFirst {
		return
	}
	row := rows[0]
	if row.ID != submitted.Task.ID || row.State != string(domain.TaskQueued) || row.Attempts != 0 ||
		row.Capability != params.Capability || row.Key != params.IdempotencyKey ||
		row.Hash != hex.EncodeToString(params.RequestHash) ||
		h00C1CanonicalJSON(t, []byte(row.Payload)) != h00C1CanonicalJSON(t, params.Payload) {
		t.Fatalf("H00 C1 submitted task row = %+v, want exact task/payload projection", row)
	}
}

func TestH00C1GrantCreateWorkerPreflightBothWinner(t *testing.T) {
	for _, preflightFirst := range []bool{true, false} {
		name := "grant-wins"
		if preflightFirst {
			name = "preflight-wins"
		}
		t.Run(name, func(t *testing.T) {
			clearQueuedTasks(t)
			client := mustCreateClient(t, adminStore, "h00-c1-grant-preflight-client")
			digest := h00Digest(0x43)
			workload := h00Policy(t, adminStore, uniqueName("h00-c1-grant-preflight"), digest[:], 10)
			created := mustCreateTask(t, adminStore, client.ID, workload.ID, uniqueName("h00-c1-grant-key"), testHash(0x44))
			claimed, err := rtStore.ClaimNextQueued(context.Background(), uuid.New(), time.Minute)
			if err != nil {
				t.Fatalf("claim H00 C1 grant/preflight task: %v", err)
			}
			if claimed.ID != created.TaskID || claimed.WorkloadSlug != workload.Name {
				t.Fatalf("claim projection = %+v, want task %s/slug %s", claimed, created.TaskID, workload.Name)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, _ = adminStore.pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, claimed.ID)
				_, _ = adminStore.pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, client.ID)
				_, _ = adminStore.pool.Exec(ctx, `DELETE FROM workload_policies WHERE id = $1`, workload.ID)
			})

			before := h00C1SnapshotFor(t, client.ID, workload.ID)
			preflight := func(ctx context.Context) error {
				_, err := rtStore.PreflightTask(ctx, PreflightParams{
					TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
					WorkloadID: workload.ID, WorkloadSlug: claimed.WorkloadSlug, Capability: "instruct",
					LocalMembership: true, ConfiguredCapability: true,
					LocalTopologyDigest: h00Digest(0x43),
				})
				return err
			}
			grant := func(ctx context.Context) error {
				return provisionerStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name)
			}
			grantKey, err := policy.ClientLockKey(client.ID)
			if err != nil {
				t.Fatal(err)
			}
			if preflightFirst {
				preflightErr, grantErr := runH00LockRace(t, grantKey, preflight, grant)
				if !errors.Is(preflightErr, ErrPreflightPolicyRejected) || grantErr != nil {
					t.Fatalf("preflight-wins errors: preflight=%v grant=%v", preflightErr, grantErr)
				}
			} else {
				grantErr, preflightErr := runH00LockRace(t, grantKey, grant, preflight)
				if grantErr != nil || preflightErr != nil {
					t.Fatalf("grant-wins errors: grant=%v preflight=%v", grantErr, preflightErr)
				}
			}

			after := h00C1SnapshotFor(t, client.ID, workload.ID)
			wantTables := []string{"grants", "attempts"}
			if preflightFirst {
				wantTables = append(wantTables, "tasks")
			}
			h00C1AssertChangedTables(t, before, after, wantTables...)
			granted, err := provisionerStore.ClientHasWorkload(context.Background(), client.ID, workload.ID)
			if err != nil || !granted {
				t.Fatalf("H00 C1 final grant = %v, err=%v; want true", granted, err)
			}
			row := h00C1TaskRows(t, client.ID, workload.ID)
			if len(row) != 1 || row[0].ID != claimed.ID || row[0].Attempts != 1 {
				t.Fatalf("H00 C1 final grant/preflight task rows = %+v", row)
			}
			attempt, err := rtStore.GetLastAttempt(context.Background(), claimed.ID)
			if err != nil {
				t.Fatalf("read H00 C1 final attempt: %v", err)
			}
			if preflightFirst {
				if row[0].State != string(domain.TaskFailed) || attempt.State != AttemptPhaseFailed ||
					attempt.ErrorClass == nil || *attempt.ErrorClass != string(domain.ErrorPolicyRejected) ||
					attempt.ErrorCode == nil || *attempt.ErrorCode != "authorization_revoked" {
					t.Fatalf("preflight-wins final task/attempt = %+v/%+v", row[0], attempt)
				}
			} else if row[0].State != string(domain.TaskRunning) || attempt.State != AttemptPhaseAuthorized {
				t.Fatalf("grant-wins final task/attempt = %+v/%+v, want running/authorized", row[0], attempt)
			}
			if h00C1Count(t, `SELECT count(*) FROM task_results r JOIN tasks t ON t.id = r.task_id WHERE t.id = $1`, claimed.ID) != 0 {
				t.Fatal("H00 C1 grant/preflight race created a result")
			}
		})
	}
}

func TestH00C1AdmissionMaxActiveOneExactWinner(t *testing.T) {
	client, workload, firstParams := h00C1StoreFixture(t, 1)
	secondParams := firstParams
	secondParams.IdempotencyKey = uniqueName("h00-c1-admission-key")
	secondParams.RequestHash = testHash(0x45)
	before := h00C1SnapshotFor(t, client.ID, workload.ID)
	var first, second SubmissionResult
	submitFirst := func(ctx context.Context) error {
		var err error
		first, err = rtStore.SubmitTask(ctx, firstParams)
		return err
	}
	submitSecond := func(ctx context.Context) error {
		var err error
		second, err = rtStore.SubmitTask(ctx, secondParams)
		return err
	}
	key, err := policy.GrantAdmissionLockKey(client.ID, workload.Name)
	if err != nil {
		t.Fatal(err)
	}
	firstErr, secondErr := runH00LockRace(t, key, submitFirst, submitSecond)
	if firstErr != nil || !first.Created {
		t.Fatalf("H00 C1 first admission = %+v, err=%v; want created", first, firstErr)
	}
	if !errors.Is(secondErr, ErrSubmissionAdmission) || second.Created {
		t.Fatalf("H00 C1 second admission = %+v, err=%v; want admission-limited/no result", second, secondErr)
	}
	after := h00C1SnapshotFor(t, client.ID, workload.ID)
	h00C1AssertChangedTables(t, before, after, "tasks", "payloads")
	if active, err := rtStore.CountActiveTasks(context.Background(), client.ID, workload.ID); err != nil || active != 1 {
		t.Fatalf("H00 C1 active count = %d, err=%v; want exactly 1", active, err)
	}
	rows := h00C1TaskRows(t, client.ID, workload.ID)
	if len(rows) != 1 || rows[0].ID != first.Task.ID || rows[0].State != string(domain.TaskQueued) || rows[0].Attempts != 0 {
		t.Fatalf("H00 C1 admission task rows = %+v, want exactly first queued task", rows)
	}
	if got := h00C1Count(t, `SELECT count(*) FROM task_attempts a JOIN tasks t ON t.id = a.task_id WHERE t.client_id = $1 AND t.workload_id = $2`, client.ID, workload.ID); got != 0 {
		t.Fatalf("H00 C1 admission attempts = %d, want 0", got)
	}
}

func TestH00C1ApplyGlobalThenLexicalLocksReverseInput(t *testing.T) {
	ctx := context.Background()
	firstName := uniqueName("h00-c1-apply-a")
	secondName := uniqueName("h00-c1-apply-b")
	firstDigest := h00Digest(0x51)
	secondDigest := h00Digest(0x52)
	desired := []policy.Workload{
		{
			Name: secondName, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 2,
			RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
			AllowedCapabilities: []string{"instruct"}, AllowedGeneration: []byte(`{}`),
			RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
			PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
			MetadataRetentionSeconds: 2592000, TopologyDigest: secondDigest,
		},
		{
			Name: firstName, PriorityClass: "normal", MaxActiveTasks: 11, MaxAttempts: 2,
			RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
			AllowedCapabilities: []string{"instruct"}, AllowedGeneration: []byte(`{}`),
			RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 61,
			PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
			MetadataRetentionSeconds: 2592000, TopologyDigest: firstDigest,
		},
	}
	globalKey := policy.GlobalPolicyApplyLockKey()
	globalHolder := newH00LockBarrier(t, globalKey)
	firstPolicyKey, err := policy.PolicyLockKey(firstName)
	if err != nil {
		t.Fatal(err)
	}
	policyHolder := newH00LockBarrier(t, firstPolicyKey)

	firstDone := make(chan error, 1)
	go func() {
		_, err := provisionerStore.ApplyPolicies(ctx, desired)
		firstDone <- err
	}()
	waitForH00C1AdvisoryState(t, globalKey, 1, 1)
	// While the global key is held, ApplyPolicies must not have acquired an
	// entity lock. This is the observable global-before-entity ordering proof.
	if granted, waiting := h00C1AdvisoryState(t, firstPolicyKey); granted != 1 || waiting != 0 {
		t.Fatalf("policy lock reached before global release: granted=%d waiting=%d", granted, waiting)
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired[1], desired[0]})
		secondDone <- err
	}()
	waitForH00C1AdvisoryState(t, globalKey, 1, 2)
	globalHolder.release(t)
	waitForH00C1AdvisoryState(t, firstPolicyKey, 1, 1)
	policyHolder.release(t)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first reverse-input apply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first apply did not complete after lexical lock release")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second reverse-input apply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second apply did not complete after lexical lock release")
	}

	for _, want := range desired {
		actual, err := provisionerStore.GetWorkloadPolicyByName(ctx, want.Name)
		if err != nil {
			t.Fatalf("read applied policy %q: %v", want.Name, err)
		}
		if actual.Name != want.Name || actual.PriorityClass != want.PriorityClass ||
			actual.MaxActiveTasks != want.MaxActiveTasks || actual.MaxAttempts != want.MaxAttempts ||
			!reflect.DeepEqual(actual.RetryDelaysSeconds, want.RetryDelaysSeconds) ||
			actual.MaxOutputTokens != want.MaxOutputTokens ||
			!reflect.DeepEqual(actual.AllowedGeneration, want.AllowedGeneration) ||
			!reflect.DeepEqual(actual.RequiredFeatures, want.RequiredFeatures) ||
			actual.WorkloadTimeoutSeconds != want.WorkloadTimeoutSeconds ||
			actual.PayloadRetentionSeconds != want.PayloadRetentionSeconds ||
			actual.ResultRetentionSeconds != want.ResultRetentionSeconds ||
			actual.MetadataRetentionSeconds != want.MetadataRetentionSeconds ||
			!reflect.DeepEqual(actual.AllowedCapabilities, want.AllowedCapabilities) ||
			!reflect.DeepEqual(actual.TopologyDigest, want.TopologyDigest[:]) {
			t.Fatalf("coherent apply projection for %q = %+v, want max_active=%d timeout=%d capabilities=%v digest=%x",
				want.Name, actual, want.MaxActiveTasks, want.WorkloadTimeoutSeconds, want.AllowedCapabilities, want.TopologyDigest)
		}
	}
}
