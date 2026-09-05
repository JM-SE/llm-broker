package compat

import (
	"bytes"
	"errors"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
)

var errInvalidConfig = errors.New("invalid configuration")

// EffectiveState is a read-only snapshot supplied by the provisioner-backed
// store. It intentionally contains no credential, URL, model, or database
// connection information.
type EffectiveState struct {
	ClientFound   bool
	ClientEnabled bool
	GrantPresent  bool
	PolicyFound   bool
	Policy        policy.Workload
}

// EvaluateDesired checks only validated configuration and a manifest. It does
// not access persistence, the environment, an adapter, or a network.
func EvaluateDesired(cfg *config.Config, manifest Manifest) (Decision, error) {
	decision := Decision{Findings: Findings{}}
	if cfg == nil {
		return decision, errInvalidConfig
	}
	if err := manifest.Validate(); err != nil {
		return decision, err
	}
	workloads, err := cfg.PolicyProjections()
	if err != nil {
		return decision, err
	}
	if !rangeContains(manifest.APIRange, CurrentAPIVersion) {
		decision.Findings.add(FindingAPIVersionIncompatible)
	}

	var workload *policy.Workload
	for i := range workloads {
		if workloads[i].Name == manifest.Workload {
			workload = &workloads[i]
			break
		}
	}
	if workload == nil {
		decision.Findings.add(FindingWorkloadUnavailable)
		return decision, nil
	}
	if !contains(workload.AllowedCapabilities, manifest.Capability) {
		decision.Findings.add(FindingCapabilityUnavailable)
		return evaluateWorkloadOnly(decision, manifest, *workload)
	}

	backend, ok := cfg.Backends[cfg.Capabilities[manifest.Capability].Backend]
	if !ok {
		// A loaded configuration cannot reach this state. Keeping the check here
		// makes direct callers fail closed without exposing route details.
		decision.Findings.add(FindingCapabilityUnavailable)
		return evaluateWorkloadOnly(decision, manifest, *workload)
	}
	evaluateWorkload(&decision, manifest, *workload)
	evaluateBackend(&decision, manifest, *workload, backend)
	return decision, nil
}

func evaluateWorkloadOnly(decision Decision, manifest Manifest, workload policy.Workload) (Decision, error) {
	evaluateWorkload(&decision, manifest, workload)
	return decision, nil
}

func evaluateWorkload(decision *Decision, manifest Manifest, workload policy.Workload) {
	if manifest.MaxRequestContentBytes > domain.MaxCombinedMessageBytes {
		decision.Findings.add(FindingRequestContentLimitInsufficient)
	}
	if !workload.MaxOutputBytesExplicit {
		decision.Findings.add(FindingExplicitOutputBytesRequired)
	} else if workload.MaxOutputBytes < manifest.RequiredMinOutputBytes {
		decision.Findings.add(FindingOutputBytesLimitInsufficient)
	}
	if workload.WorkloadTimeoutSeconds < manifest.MinWorkloadTimeoutSeconds {
		decision.Findings.add(FindingWorkloadTimeoutInsufficient)
	}
	if workload.MaxAttempts > manifest.MaxAcceptableAttempts {
		decision.Findings.add(FindingAttemptLimitExceeded)
	}
	if manifest.ConsumerOverallDeadlineSeconds <= workload.WorkloadTimeoutSeconds {
		decision.Findings.add(FindingConsumerDeadlineTooShort)
	}
	if err := policy.ValidateGeneration(manifest.Generation, workload.AllowedGeneration, workload.MaxOutputTokens); err != nil {
		decision.Findings.add(FindingGenerationNotAdmitted)
	}
}

func evaluateBackend(decision *Decision, manifest Manifest, workload policy.Workload, backend config.BackendConfig) {
	for _, required := range workload.RequiredFeatures {
		if !contains(backend.Features, required) {
			decision.Findings.add(FindingCapabilityFeaturesUnsupported)
			break
		}
	}
	structuredOutput := backend.Type == config.AdapterFake || contains(backend.Features, config.FeatureStructuredOutput)
	if manifest.Response.Kind != domain.ResponseText && !structuredOutput {
		if manifest.Response.Kind == domain.ResponseJSONSchema || manifest.Response.SchemaRequired {
			decision.Findings.add(FindingResponseSchemaUnsupported)
		} else {
			decision.Findings.add(FindingResponseKindUnsupported)
		}
	}
	if manifest.MinBackendResponseStartTimeoutSeconds > 0 {
		if backend.Type != config.AdapterOpenAICompatible {
			decision.Findings.add(FindingBackendResponseStartTimeoutInsuff)
		} else {
			timeout := backend.ResponseHeaderTimeoutSeconds
			if timeout == 0 {
				timeout = config.DefaultResponseHeaderTimeoutSeconds
			}
			if timeout < manifest.MinBackendResponseStartTimeoutSeconds {
				decision.Findings.add(FindingBackendResponseStartTimeoutInsuff)
			}
		}
	}
}

// EvaluateEffective evaluates desired compatibility and then adds checks for
// the supplied durable snapshot. No durable state is changed.
func EvaluateEffective(cfg *config.Config, manifest Manifest, state EffectiveState) (Decision, error) {
	decision, err := EvaluateDesired(cfg, manifest)
	if err != nil {
		return decision, err
	}
	if !state.ClientFound {
		decision.Findings.add(FindingClientNotFound)
		return decision, nil
	}
	if !state.ClientEnabled {
		decision.Findings.add(FindingClientDisabled)
	}
	if !state.PolicyFound {
		decision.Findings.add(FindingDurablePolicyDrift)
		return decision, nil
	}
	if !state.GrantPresent {
		decision.Findings.add(FindingWorkloadGrantMissing)
	}

	workloads, err := cfg.PolicyProjections()
	if err != nil {
		return decision, err
	}
	var desired *policy.Workload
	for i := range workloads {
		if workloads[i].Name == manifest.Workload {
			desired = &workloads[i]
			break
		}
	}
	if desired == nil {
		decision.Findings.add(FindingDurablePolicyDrift)
		return decision, nil
	}
	if !bytes.Equal(state.Policy.TopologyDigest[:], desired.TopologyDigest[:]) {
		decision.Findings.add(FindingTopologyMismatch)
	}
	// Topology and mutable-policy drift are reported independently: a
	// topology change does not mask a mutable bound change and vice versa.
	// The digest itself is normalized away before the semantic comparison so
	// only effective policy fields drive the drift finding.
	actual := state.Policy
	actual.TopologyDigest = desired.TopologyDigest
	if !actual.SemanticallyEqual(*desired) {
		decision.Findings.add(FindingDurablePolicyDrift)
	}

	// The durable row is the runtime authority for these bounds. Re-checking
	// them catches a policy edit even when the configuration file is unchanged.
	if !state.Policy.MaxOutputBytesExplicit {
		decision.Findings.add(FindingExplicitOutputBytesRequired)
	} else if state.Policy.MaxOutputBytes < manifest.RequiredMinOutputBytes {
		decision.Findings.add(FindingOutputBytesLimitInsufficient)
	}
	if state.Policy.MaxAttempts > manifest.MaxAcceptableAttempts {
		decision.Findings.add(FindingAttemptLimitExceeded)
	}
	if state.Policy.WorkloadTimeoutSeconds < manifest.MinWorkloadTimeoutSeconds {
		decision.Findings.add(FindingWorkloadTimeoutInsufficient)
	}
	if !contains(state.Policy.AllowedCapabilities, manifest.Capability) {
		decision.Findings.add(FindingCapabilityUnavailable)
	}
	if err := policy.ValidateGeneration(manifest.Generation, state.Policy.AllowedGeneration, state.Policy.MaxOutputTokens); err != nil {
		decision.Findings.add(FindingGenerationNotAdmitted)
	}
	return decision, nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
