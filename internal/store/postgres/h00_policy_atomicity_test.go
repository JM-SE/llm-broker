package postgres

import (
	"context"
	"reflect"
	"testing"

	"github.com/JM-SE/llm-broker/internal/policy"
)

func TestApplyPoliciesRollsBackOnRejectingCapabilityTrigger(t *testing.T) {
	ctx := context.Background()
	name := uniqueName("h00-atomic-policy")
	digest := [32]byte{0x81}
	initial := policy.Workload{
		Name: name, PriorityClass: "normal", MaxActiveTasks: 10, MaxAttempts: 2,
		RetryDelaysSeconds: []int{1}, MaxOutputTokens: 128,
		AllowedCapabilities: []string{"instruct"}, AllowedGeneration: []byte(`{}`),
		RequiredFeatures: []string{}, WorkloadTimeoutSeconds: 60,
		PayloadRetentionSeconds: 3600, ResultRetentionSeconds: 86400,
		MetadataRetentionSeconds: 2592000, TopologyDigest: digest,
	}
	if _, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{initial}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	const functionName = "h00_test_reject_capability"
	const triggerName = "h00_test_reject_capability_trigger"
	_, err := adminStore.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION `+functionName+`()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.capability = 'reject-me' THEN
				RAISE EXCEPTION 'H00 test rejection';
			END IF;
			RETURN NEW;
		END
		$$;
		CREATE TRIGGER `+triggerName+`
		BEFORE INSERT ON workload_policy_capabilities
		FOR EACH ROW EXECUTE FUNCTION `+functionName+`();`)
	if err != nil {
		t.Fatalf("install rejecting trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminStore.pool.Exec(ctx,
			`DROP TRIGGER IF EXISTS `+triggerName+` ON workload_policy_capabilities;
			DROP FUNCTION IF EXISTS `+functionName+`();`)
	})

	desired := initial
	desired.MaxActiveTasks = 11
	desired.AllowedCapabilities = []string{"instruct", "reject-me"}
	if _, err := provisionerStore.ApplyPolicies(ctx, []policy.Workload{desired}); err == nil {
		t.Fatal("policy apply succeeded despite rejecting capability trigger")
	}

	actual, err := adminStore.GetWorkloadPolicyByName(ctx, name)
	if err != nil {
		t.Fatalf("read policy after rollback: %v", err)
	}
	if !reflect.DeepEqual(actual.AllowedCapabilities, initial.AllowedCapabilities) {
		t.Fatalf("capabilities after rollback = %v, want %v", actual.AllowedCapabilities, initial.AllowedCapabilities)
	}
	if actual.MaxActiveTasks != initial.MaxActiveTasks || actual.TopologyDigest == nil ||
		!reflect.DeepEqual(actual.TopologyDigest, initial.TopologyDigest[:]) {
		t.Fatalf("policy mutation was not rolled back: got %+v, want max_active_tasks=%d/topology=%x",
			actual, initial.MaxActiveTasks, initial.TopologyDigest)
	}
}
