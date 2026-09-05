// Package testsupport contains fixtures shared by integration-test packages.
// It deliberately uses the production ApplyPolicies path rather than exposing
// a compatibility mutation API from the postgres store.
package testsupport

import (
	"context"

	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// ApplyPolicy applies one durable policy through the same all-policy
// transaction used by the provisioner and returns its effective row.
func ApplyPolicy(ctx context.Context, store *postgres.Store, input postgres.WorkloadPolicy) (postgres.WorkloadPolicy, error) {
	var digest [32]byte
	if len(input.TopologyDigest) == len(digest) {
		copy(digest[:], input.TopologyDigest)
	}
	_, err := store.ApplyPolicies(ctx, []policy.Workload{{
		Name:                     input.Name,
		PriorityClass:            input.PriorityClass,
		MaxActiveTasks:           input.MaxActiveTasks,
		MaxAttempts:              input.MaxAttempts,
		RetryDelaysSeconds:       append([]int(nil), input.RetryDelaysSeconds...),
		MaxOutputTokens:          input.MaxOutputTokens,
		MaxOutputBytes:           input.MaxOutputBytes,
		MaxOutputBytesExplicit:   input.MaxOutputBytesExplicit,
		AllowedCapabilities:      append([]string(nil), input.AllowedCapabilities...),
		AllowedGeneration:        append([]byte(nil), input.AllowedGeneration...),
		RequiredFeatures:         append([]string(nil), input.RequiredFeatures...),
		WorkloadTimeoutSeconds:   input.WorkloadTimeoutSeconds,
		PayloadRetentionSeconds:  input.PayloadRetentionSeconds,
		ResultRetentionSeconds:   input.ResultRetentionSeconds,
		MetadataRetentionSeconds: input.MetadataRetentionSeconds,
		TopologyDigest:           digest,
	}})
	if err != nil {
		return postgres.WorkloadPolicy{}, err
	}
	return store.GetWorkloadPolicyByName(ctx, input.Name)
}
