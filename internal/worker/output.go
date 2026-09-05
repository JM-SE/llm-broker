package worker

import (
	"encoding/json"
	"fmt"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/policy"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

// validateOutput enforces the size bound and the response contract before
// any output is persisted. The byte limit is resolved by policy: an explicit
// workload bound, otherwise the exact legacy fallback. Content length is
// UTF-8 bytes (Go string length), never a token estimate. Reasoning content
// never exists in this type.
func validateOutput(workload postgres.WorkloadPolicy, rc domain.ResponseContract, resp *backend.Response) error {
	maxContent := policy.EffectiveOutputBytes(
		workload.MaxOutputTokens, workload.MaxOutputBytes, workload.MaxOutputBytesExplicit)
	if int64(len(resp.Content)) > maxContent {
		return &outputValidationError{Code: "output_bytes_exceeded", message: fmt.Sprintf("output exceeds the workload byte limit of %d bytes", maxContent)}
	}
	switch rc.Kind {
	case domain.ResponseText:
		return nil
	case domain.ResponseJSONObject:
		if !json.Valid([]byte(resp.Content)) {
			return fmt.Errorf("output is not valid JSON")
		}
		return nil
	case domain.ResponseJSONSchema:
		if rc.Schema == nil {
			return fmt.Errorf("response contract requires a schema")
		}
		return rc.Schema.Validate([]byte(resp.Content))
	}
	return fmt.Errorf("unsupported response kind %q", rc.Kind)
}

type outputValidationError struct {
	Code    string
	message string
}

func (e *outputValidationError) Error() string { return e.message }
