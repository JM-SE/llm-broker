package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/JM-SE/llm-broker/internal/policy"
)

// h03Policy applies a workload policy with an explicit output-byte bound so
// the durable read path can be compared with the legacy fallback.
func h03Policy(t *testing.T, store *Store, name string, digest []byte, maxOutputBytes int) WorkloadPolicy {
	t.Helper()
	return applyPolicy(t, store, WorkloadPolicy{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              3,
		RetryDelaysSeconds:       []int{1, 5},
		MaxOutputTokens:          1024,
		MaxOutputBytes:           maxOutputBytes,
		MaxOutputBytesExplicit:   maxOutputBytes > 0,
		AllowedGeneration:        []byte(`{"max_tokens":{"max":1024},"temperature":{"min":0,"max":2}}`),
		RequiredFeatures:         []string{},
		WorkloadTimeoutSeconds:   300,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      []string{"instruct"},
		TopologyDigest:           digest,
	})
}

// TestH03EmptyRetryDelaysPersistAsEmptySet is the regression test for the
// acceptance blocker where a max_attempts=1 workload (H03/G frozen Y01
// profile, where the config validator forbids any retry entry) failed policy
// apply with `null value in column "retry_delays_seconds" violates not-null
// constraint`. A nil retry-delay set reached the INSERT verbatim and pgx
// encoded it as NULL. The persistence boundary must coerce nil to the empty
// set, mirroring the required_features normalization.
func TestH03EmptyRetryDelaysPersistAsEmptySet(t *testing.T) {
	ctx := context.Background()
	digest := h00Digest(0x72)
	name := uniqueName("h03-no-retry")
	desired := policy.Workload{
		Name:                     name,
		PriorityClass:            "normal",
		MaxActiveTasks:           10,
		MaxAttempts:              1,
		RetryDelaysSeconds:       nil,
		MaxOutputTokens:          1024,
		MaxOutputBytes:           16384,
		MaxOutputBytesExplicit:   true,
		AllowedGeneration:        []byte(`{"max_tokens":{"max":1024},"temperature":{"min":0,"max":2}}`),
		RequiredFeatures:         nil,
		WorkloadTimeoutSeconds:   300,
		PayloadRetentionSeconds:  3600,
		ResultRetentionSeconds:   86400,
		MetadataRetentionSeconds: 2592000,
		AllowedCapabilities:      []string{"summarize"},
		TopologyDigest:           digest,
	}

	if _, err := adminStore.ApplyPolicies(ctx, []policy.Workload{desired}); err != nil {
		t.Fatalf("apply max_attempts=1 policy with nil retry delays: %v", err)
	}
	p, err := adminStore.GetWorkloadPolicyByName(ctx, name)
	if err != nil {
		t.Fatalf("read applied policy: %v", err)
	}
	if p.MaxAttempts != 1 {
		t.Fatalf("MaxAttempts = %d, want 1", p.MaxAttempts)
	}
	if len(p.RetryDelaysSeconds) != 0 {
		t.Fatalf("RetryDelaysSeconds = %v, want empty set", p.RetryDelaysSeconds)
	}

	// Re-applying the same desired projection must be a no-op: nil and '{}'
	// are semantically equal, so this must not flap into an endless update.
	summary, err := adminStore.ApplyPolicies(ctx, []policy.Workload{desired})
	if err != nil {
		t.Fatalf("re-apply max_attempts=1 policy: %v", err)
	}
	if summary.Update != 0 || summary.Unchanged != 1 {
		t.Fatalf("re-apply summary = %+v, want update=0 unchanged=1", summary)
	}
}

// TestH03SubmissionPolicyProjectsExplicitOutputBytes proves the durable
// max_output_bytes reaches the runtime through both readSubmissionPolicy
// paths (SubmitTask and PreflightTask), so an explicit 16384-byte workload is
// never silently downgraded to the legacy max_output_tokens*4 bound.
func TestH03SubmissionPolicyProjectsExplicitOutputBytes(t *testing.T) {
	ctx := context.Background()
	digest := h00Digest(0x71)

	explicit := h03Policy(t, adminStore, uniqueName("h03-explicit"), digest[:], 16384)
	legacy := h03Policy(t, adminStore, uniqueName("h03-legacy"), digest[:], 0)

	for _, tc := range []struct {
		name             string
		workload         WorkloadPolicy
		wantBytes        int
		wantExplicitFlag bool
	}{{
		name: "explicit durable value", workload: explicit, wantBytes: 16384, wantExplicitFlag: true,
	}, {
		name: "NULL preserves legacy", workload: legacy, wantBytes: 0, wantExplicitFlag: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			clearQueuedTasks(t)
			p, err := adminStore.GetWorkloadPolicyByName(ctx, tc.workload.Name)
			if err != nil {
				t.Fatalf("read durable policy: %v", err)
			}
			if p.MaxOutputBytes != tc.wantBytes || p.MaxOutputBytesExplicit != tc.wantExplicitFlag {
				t.Fatalf("policy read projection = (%d,%v), want (%d,%v)",
					p.MaxOutputBytes, p.MaxOutputBytesExplicit, tc.wantBytes, tc.wantExplicitFlag)
			}

			client := mustCreateClient(t, adminStore, uniqueName("h03-client"))
			if err := adminStore.GrantWorkloadBySlug(ctx, client.ID, tc.workload.Name); err != nil {
				t.Fatalf("grant workload: %v", err)
			}
			_, err = rtStore.SubmitTask(ctx, h00SubmitParams(t, client, tc.workload, uniqueName("h03-key"), testHash(0x9)))
			if err != nil {
				t.Fatalf("submit task: %v", err)
			}
			claimed, err := rtStore.ClaimNextQueued(ctx, uuid.New(), time.Minute)
			if err != nil {
				t.Fatalf("claim task: %v", err)
			}
			runtime, err := rtStore.PreflightTask(ctx, PreflightParams{
				TaskID: claimed.ID, Attempt: claimed.Attempts, ClientID: client.ID,
				WorkloadID: tc.workload.ID, WorkloadSlug: tc.workload.Name, Capability: "instruct",
				LocalMembership: true, ConfiguredCapability: true,
				LocalTopologyDigest: digest,
			})
			if err != nil {
				t.Fatalf("preflight task: %v", err)
			}
			if runtime.MaxOutputBytes != tc.wantBytes || runtime.MaxOutputBytesExplicit != tc.wantExplicitFlag {
				t.Fatalf("preflight policy projection = (%d,%v), want (%d,%v)",
					runtime.MaxOutputBytes, runtime.MaxOutputBytesExplicit, tc.wantBytes, tc.wantExplicitFlag)
			}
			// Frozen Y01 guard: explicit 1024-token/16384-byte workloads must
			// never carry the legacy 4096-byte derivation into the worker.
			if tc.wantExplicitFlag && runtime.MaxOutputBytes != 16384 {
				t.Fatalf("explicit workload runtime bound = %d, want 16384", runtime.MaxOutputBytes)
			}
			if _, err := rtStore.FinishTaskFailed(ctx, claimed.ID, claimed.Attempts, "internal", "h03_test_cleanup"); err != nil {
				t.Fatalf("finish authorized test task: %v", err)
			}
		})
	}
}
