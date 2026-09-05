package config

import (
	"fmt"

	"github.com/JM-SE/llm-broker/internal/policy"
)

// Topology returns the process-local routing facts used for startup fencing
// and policy provisioning. Credential environment names and values are never
// included in the digest.
func (c *Config) Topology() policy.Topology {
	capabilities := make(map[string]string, len(c.Capabilities))
	for name, capability := range c.Capabilities {
		capabilities[name] = capability.Backend
	}
	backends := make(map[string]policy.Backend, len(c.Backends))
	for name, backend := range c.Backends {
		backends[name] = policy.Backend{
			Type: backend.Type, BaseURL: backend.BaseURL, Model: backend.Model,
			Features: append([]string(nil), backend.Features...),
		}
	}
	return policy.Topology{Capabilities: capabilities, Backends: backends}
}

// TopologyDigest computes the local secret-free topology identity.
func (c *Config) TopologyDigest() [32]byte { return policy.Digest(c.Topology()) }

// PolicyProjections returns validated desired workload rows in canonical
// application form. It is intentionally a pure conversion and performs no
// database work.
func (c *Config) PolicyProjections() ([]policy.Workload, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	digest := c.TopologyDigest()
	out := make([]policy.Workload, 0, len(c.Workloads))
	for name, workload := range c.Workloads {
		generation := workload.AllowedGeneration
		if generation == nil {
			generation = map[string]GenerationConstraint{}
		}
		canonicalGeneration, err := policy.CanonicalGeneration(generation)
		if err != nil {
			return nil, fmt.Errorf("workloads.%s.allowed_generation: %w", name, err)
		}
		retention := c.Retention
		if workload.Retention != nil {
			retention = *workload.Retention
		}
		requiredFeatures := append([]string{}, workload.RequiredFeatures...)
		out = append(out, policy.Workload{
			Name: name, PriorityClass: workload.PriorityClass,
			MaxActiveTasks: workload.MaxActiveTasks, MaxAttempts: workload.MaxAttempts,
			RetryDelaysSeconds:       append([]int(nil), workload.RetryDelaysSeconds...),
			MaxOutputTokens:          workload.MaxOutputTokens,
			MaxOutputBytes:           workload.MaxOutputBytes,
			MaxOutputBytesExplicit:   workload.MaxOutputBytes > 0,
			AllowedCapabilities:      append([]string(nil), workload.AllowedCapabilities...),
			AllowedGeneration:        canonicalGeneration,
			RequiredFeatures:         requiredFeatures,
			WorkloadTimeoutSeconds:   workload.WorkloadTimeoutSeconds,
			PayloadRetentionSeconds:  int64(retention.Payload.Duration.Seconds()),
			ResultRetentionSeconds:   int64(retention.Result.Duration.Seconds()),
			MetadataRetentionSeconds: int64(retention.Metadata.Duration.Seconds()),
			TopologyDigest:           digest,
		})
	}
	return out, nil
}
