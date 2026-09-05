package compat

import "sort"

// FindingCode is a stable machine-readable compatibility condition.
type FindingCode string

const (
	FindingAPIVersionIncompatible                  FindingCode = "api_version_incompatible"
	FindingWorkloadUnavailable                     FindingCode = "workload_unavailable"
	FindingCapabilityUnavailable                   FindingCode = "capability_unavailable"
	FindingCapabilityFeaturesUnsupported           FindingCode = "capability_features_unsupported"
	FindingGenerationNotAdmitted                   FindingCode = "generation_not_admitted"
	FindingResponseKindUnsupported                 FindingCode = "response_kind_unsupported"
	FindingResponseSchemaUnsupported               FindingCode = "response_schema_unsupported"
	FindingRequestContentLimitInsufficient         FindingCode = "request_content_limit_insufficient"
	FindingExplicitOutputBytesRequired             FindingCode = "explicit_output_bytes_required"
	FindingOutputBytesLimitInsufficient            FindingCode = "output_bytes_limit_insufficient"
	FindingBackendResponseStartTimeoutInsufficient FindingCode = "backend_response_start_timeout_insufficient"
	FindingWorkloadTimeoutInsufficient             FindingCode = "workload_timeout_insufficient"
	FindingAttemptLimitExceeded                    FindingCode = "attempt_limit_exceeded"
	FindingConsumerDeadlineTooShort                FindingCode = "consumer_deadline_too_short"
	FindingClientNotFound                          FindingCode = "client_not_found"
	FindingClientDisabled                          FindingCode = "client_disabled"
	FindingWorkloadGrantMissing                    FindingCode = "workload_grant_missing"
	FindingDurablePolicyDrift                      FindingCode = "durable_policy_drift"
	FindingTopologyMismatch                        FindingCode = "topology_mismatch"
)

// FindingBackendResponseStartTimeoutInsuff is retained as a short spelling
// for callers that used the initial implementation name.
const FindingBackendResponseStartTimeoutInsuff = FindingBackendResponseStartTimeoutInsufficient

// Findings stores counts rather than prose so output is deterministic and
// cannot accidentally disclose configuration or durable-state values.
type Findings map[FindingCode]int

func (f Findings) add(code FindingCode) { f[code]++ }

// Sorted returns findings in lexical code order.
func (f Findings) Sorted() []Finding {
	out := make([]Finding, 0, len(f))
	for code, count := range f {
		out = append(out, Finding{Code: code, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

type Finding struct {
	Code  FindingCode
	Count int
}

// Decision is the pure result of a desired or effective evaluation.
type Decision struct{ Findings Findings }

func (d Decision) Compatible() bool { return len(d.Findings) == 0 }
