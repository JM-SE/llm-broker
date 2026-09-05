package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
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
