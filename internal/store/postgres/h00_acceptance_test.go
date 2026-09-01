package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/google/uuid"
)

func h00Policy(t *testing.T, store *Store, name string, digest []byte, maxActive int) WorkloadPolicy {
	t.Helper()
	p := applyPolicy(t, store, WorkloadPolicy{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           maxActive,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1, 5},
		MaxOutputTokens:          2048,
		AllowedGeneration:        []byte(`{"max_tokens":{"max":2048}}`),
		RequiredFeatures:         []string{},
		WorkloadTimeoutSeconds:   300,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      []string{"instruct"},
		TopologyDigest:           digest,
	})
	return p
}

func h00Submission(t *testing.T, workload string) (*domain.TaskSubmission, []byte) {
	t.Helper()
	sub := &domain.TaskSubmission{
		Workload:   workload,
		Capability: "instruct",
		Messages:   []domain.Message{{Role: domain.RoleUser, Content: "h00 acceptance"}},
		Response:   domain.ResponseContract{Kind: domain.ResponseText},
	}
	payload, err := sub.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical submission: %v", err)
	}
	return sub, payload
}

func h00Proof(c Client) [32]byte {
	var proof [32]byte
	copy(proof[:], c.CredentialDigest)
	return proof
}

func h00Digest(b byte) [32]byte {
	var digest [32]byte
	for i := range digest {
		digest[i] = b
	}
	return digest
}

func h00SubmitParams(t *testing.T, c Client, p WorkloadPolicy, key string, hash []byte) SubmissionParams {
	t.Helper()
	sub, payload := h00Submission(t, p.Name)
	var topology [32]byte
	copy(topology[:], p.TopologyDigest)
	return SubmissionParams{
		ClientID:              c.ID,
		CredentialDigestProof: h00Proof(c),
		WorkloadSlug:          p.Name,
		Capability:            "instruct",
		IdempotencyKey:        key,
		RequestHash:           hash,
		Payload:               payload,
		Submission:            sub,
		LocalMembership:       true,
		CapabilityKnown:       true,
		LocalTopologyDigest:   topology,
		ConfiguredTopologies:  []ConfiguredWorkloadTopology{{Slug: p.Name, Digest: topology}},
	}
}

func TestH00AtomicSubmissionAuthorizationPrecedence(t *testing.T) {
	clearQueuedTasks(t)
	client := mustCreateClient(t, adminStore, "h00-submit-client")
	digest := testHash(0x51)
	workload := h00Policy(t, adminStore, uniqueName("h00-submit-workload"), digest, 1)
	if err := adminStore.GrantWorkloadBySlug(context.Background(), client.ID, workload.Name); err != nil {
		t.Fatalf("grant workload: %v", err)
	}

	ctx := context.Background()
	firstParams := h00SubmitParams(t, client, workload, "h00-submit-key-01", testHash(1))
	first, err := rtStore.SubmitTask(ctx, firstParams)
	if err != nil || !first.Created {
		t.Fatalf("authorized submission = %+v, err=%v", first, err)
	}

	var before int
	if err := adminStore.pool.QueryRow(ctx,
		"SELECT count(*) FROM tasks WHERE client_id=$1", client.ID).Scan(&before); err != nil {
		t.Fatalf("count before denial: %v", err)
	}
	if err := adminStore.RevokeWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("revoke workload: %v", err)
	}

	_, err = rtStore.SubmitTask(ctx, firstParams)
	if !errors.Is(err, ErrSubmissionForbidden) {
		t.Fatalf("revoked replay error = %v, want forbidden", err)
	}
	denied := firstParams
	denied.IdempotencyKey = "h00-submit-key-02"
	denied.RequestHash = testHash(2)
	_, err = rtStore.SubmitTask(ctx, denied)
	if !errors.Is(err, ErrSubmissionForbidden) {
		t.Fatalf("revoked new-key error = %v, want forbidden", err)
	}
	var after int
	if err := adminStore.pool.QueryRow(ctx,
		"SELECT count(*) FROM tasks WHERE client_id=$1", client.ID).Scan(&after); err != nil {
		t.Fatalf("count after denial: %v", err)
	}
	if after != before {
		t.Fatalf("denied submissions changed task count: before=%d after=%d", before, after)
	}

	if err := adminStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("regrant workload: %v", err)
	}
	conflict := firstParams
	conflict.RequestHash = testHash(3)
	_, err = rtStore.SubmitTask(ctx, conflict)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("authorized conflicting replay error = %v, want conflict", err)
	}
}

func TestH00SubmissionTopologyPrecedesAuthorizationDenial(t *testing.T) {
	ctx := context.Background()
	client := mustCreateClient(t, adminStore, "h00-topology-precedence-client")
	digest := h00Digest(0x81)
	workload := h00Policy(t, adminStore, uniqueName("h00-topology-precedence"), digest[:], 10)

	params := h00SubmitParams(t, client, workload, "h00-topology-precedence-key", testHash(0x82))
	params.LocalMembership = false
	params.CapabilityKnown = false
	params.ConfiguredTopologies = []ConfiguredWorkloadTopology{{
		Slug:   workload.Name,
		Digest: h00Digest(0x83),
	}}

	_, err := rtStore.SubmitTask(ctx, params)
	if !errors.Is(err, ErrSubmissionTopology) {
		t.Fatalf("topology incoherence with authorization facts denied as %v, want operational topology error", err)
	}
	var tasks int
	if err := adminStore.pool.QueryRow(ctx,
		"SELECT count(*) FROM tasks WHERE client_id=$1 AND idempotency_key=$2", client.ID, params.IdempotencyKey).Scan(&tasks); err != nil {
		t.Fatalf("count topology-denied tasks: %v", err)
	}
	if tasks != 0 {
		t.Fatalf("topology-denied submission created %d task rows", tasks)
	}
}

func TestH00PreflightRevocationAndSafeClaimRecovery(t *testing.T) {
	clearQueuedTasks(t)
	client := mustCreateClient(t, adminStore, "h00-preflight-client")
	digest := testHash(0x61)
	workload := h00Policy(t, adminStore, uniqueName("h00-preflight-workload"), digest, 10)
	ctx := context.Background()
	if err := adminStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("grant workload: %v", err)
	}

	first := mustCreateTask(t, adminStore, client.ID, workload.ID, "h00-preflight-key-1", testHash(4))
	claimed, err := rtStore.ClaimNextQueued(ctx, uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim first task: %v", err)
	}
	if claimed.ID != first.TaskID || claimed.WorkloadSlug != workload.Name {
		t.Fatalf("claim projection = %+v, want task %s/workload %s", claimed, first.TaskID, workload.Name)
	}
	if err := adminStore.RevokeWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("revoke before preflight: %v", err)
	}
	_, err = rtStore.PreflightTask(ctx, PreflightParams{
		TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
		WorkloadID: workload.ID, WorkloadSlug: workload.Name, Capability: "instruct",
		LocalMembership: true, ConfiguredCapability: true,
		LocalTopologyDigest: h00Digest(0x61),
	})
	if !errors.Is(err, ErrPreflightPolicyRejected) {
		t.Fatalf("revoked preflight error = %v, want policy rejection", err)
	}
	failedTask, err := rtStore.GetTask(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("read denied task: %v", err)
	}
	if failedTask.State != domain.TaskFailed {
		t.Fatalf("denied task state = %s, want failed", failedTask.State)
	}
	attempt, err := rtStore.GetLastAttempt(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("read denied attempt: %v", err)
	}
	if attempt.State != "failed" || attempt.ErrorClass == nil || attempt.ErrorCode == nil ||
		*attempt.ErrorClass != string(domain.ErrorPolicyRejected) || *attempt.ErrorCode != "authorization_revoked" {
		t.Fatalf("denied attempt = %+v", attempt)
	}

	if err := adminStore.GrantWorkloadBySlug(ctx, client.ID, workload.Name); err != nil {
		t.Fatalf("regrant workload: %v", err)
	}
	second := mustCreateTask(t, adminStore, client.ID, workload.ID, "h00-preflight-key-2", testHash(5))
	claimed, err = rtStore.ClaimNextQueued(ctx, uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim second task: %v", err)
	}
	if claimed.ID != second.TaskID {
		t.Fatalf("second claim = %s, want %s", claimed.ID, second.TaskID)
	}
	_, err = rtStore.PreflightTask(ctx, PreflightParams{
		TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
		WorkloadID: workload.ID, WorkloadSlug: workload.Name, Capability: "instruct",
		LocalMembership: true, ConfiguredCapability: true,
		LocalTopologyDigest: h00Digest(0x61),
	})
	if err != nil {
		t.Fatalf("authorized preflight: %v", err)
	}
	attempt, err = rtStore.GetLastAttempt(ctx, claimed.ID)
	if err != nil || attempt.State != "authorized" {
		t.Fatalf("authorized attempt = %+v, err=%v", attempt, err)
	}
	if _, err := rtStore.FinishTaskFailed(ctx, claimed.ID, claimed.Attempts, string(domain.ErrorInternal), "h00_test_cleanup"); err != nil {
		t.Fatalf("finish authorized test task: %v", err)
	}

	third := mustCreateTask(t, adminStore, client.ID, workload.ID, "h00-preflight-key-3", testHash(6))
	claimed, err = rtStore.ClaimNextQueued(ctx, uuid.New(), time.Minute)
	if err != nil {
		t.Fatalf("claim third task: %v", err)
	}
	if claimed.ID != third.TaskID {
		t.Fatalf("third claim = %s, want %s", claimed.ID, third.TaskID)
	}
	_, err = rtStore.PreflightTask(ctx, PreflightParams{
		TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
		WorkloadID: workload.ID, WorkloadSlug: workload.Name, Capability: "instruct",
		LocalMembership: true, ConfiguredCapability: true,
		LocalTopologyDigest: [32]byte{0x62},
	})
	if !errors.Is(err, ErrPreflightOperational) {
		t.Fatalf("topology mismatch error = %v, want operational failure", err)
	}
	if _, err := adminStore.pool.Exec(ctx,
		"UPDATE tasks SET lease_until=now()-interval '1 second' WHERE id=$1", claimed.ID); err != nil {
		t.Fatalf("expire claimed lease: %v", err)
	}
	items, err := rtStore.ListUnresolvedRecovery(ctx)
	if err != nil {
		t.Fatalf("list safe recovery: %v", err)
	}
	var found *RecoveryItem
	for i := range items {
		if items[i].Task.ID == claimed.ID {
			found = &items[i]
			break
		}
	}
	if found == nil || !found.SafeToRequeue || found.Phase != AttemptPhaseClaimed {
		t.Fatalf("claimed recovery = %+v, want safe claimed item", found)
	}
	if _, err := rtStore.RequeueForRetry(ctx, claimed.ID, claimed.Attempts,
		string(domain.ErrorTransientUnsent), "lease_expired_safe_unsent", time.Now()); err != nil {
		t.Fatalf("safe requeue: %v", err)
	}
}

func TestH00PolicyApplyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	name := uniqueName("h00-policy-apply")
	digest := [32]byte{0x71}
	desired := policy.Workload{
		Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 2,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
		AllowedCapabilities: []string{"instruct"}, AllowedGeneration: []byte(`{"temperature":{"min":1.0}}`),
		RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000, TopologyDigest: digest,
	}
	first, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired})
	if err != nil {
		t.Fatalf("first policy apply: %v", err)
	}
	if first.Create != 1 {
		t.Fatalf("first policy report = %+v", first)
	}
	actual, err := provisionerStore.GetWorkloadPolicyByName(ctx, name)
	if err != nil {
		t.Fatalf("read applied policy: %v", err)
	}
	updatedAt := actual.UpdatedAt
	desired.AllowedGeneration = []byte(`{"temperature":{"min":1}}`)
	second, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired})
	if err != nil {
		t.Fatalf("second policy apply: %v", err)
	}
	if second.Unchanged != 1 || second.Create != 0 || second.Update != 0 {
		t.Fatalf("second policy report = %+v", second)
	}
	actual, err = provisionerStore.GetWorkloadPolicyByName(ctx, name)
	if err != nil {
		t.Fatalf("read unchanged policy: %v", err)
	}
	if !actual.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("no-op apply changed updated_at: before=%v after=%v", updatedAt, actual.UpdatedAt)
	}
	status, err := provisionerStore.StatusPolicies(ctx, []policy.Workload{desired})
	if err != nil {
		t.Fatalf("policy status: %v", err)
	}
	if status.Unchanged != 1 || status.TopologyMismatch != 0 {
		t.Fatalf("policy status = %+v", status)
	}
}

func TestH00PolicyApplyReportsTopologyMismatch(t *testing.T) {
	ctx := context.Background()
	name := uniqueName("h00-policy-topology-report")
	firstDigest := h00Digest(0x91)
	desired := policy.Workload{
		Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 2,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
		AllowedCapabilities: []string{"instruct"}, AllowedGeneration: []byte(`{"temperature":{"min":1}}`),
		RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000, TopologyDigest: firstDigest,
	}
	if report, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired}); err != nil {
		t.Fatalf("initial topology policy apply: %v", err)
	} else if report.Create != 1 || report.TopologyMismatch != 0 {
		t.Fatalf("initial topology policy report = %+v", report)
	}

	desired.TopologyDigest = h00Digest(0x92)
	report, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired})
	if err != nil {
		t.Fatalf("changed topology policy apply: %v", err)
	}
	if report.TopologyMismatch != 1 || report.Update != 0 || report.Unchanged != 0 {
		t.Fatalf("changed topology policy report = %+v, want topology-mismatch only", report)
	}
	actual, err := provisionerStore.GetWorkloadPolicyByName(ctx, name)
	if err != nil {
		t.Fatalf("read changed topology policy: %v", err)
	}
	if len(actual.TopologyDigest) != 32 || actual.TopologyDigest[0] != 0x92 {
		t.Fatalf("persisted topology digest = %x, want 0x92 digest", actual.TopologyDigest)
	}
}
