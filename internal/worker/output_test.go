package worker

import (
	"errors"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/domain"
	"github.com/JM-SE/llm-broker/internal/store/postgres"
)

func outputPolicy(maxTokens, maxBytes int, explicit bool) postgres.WorkloadPolicy {
	return postgres.WorkloadPolicy{
		MaxOutputTokens:        maxTokens,
		MaxOutputBytes:         maxBytes,
		MaxOutputBytesExplicit: explicit,
	}
}

func outputResponse(content string) *backend.Response {
	return &backend.Response{
		Content:      content,
		FinishReason: domain.FinishStop,
		Usage:        domain.Usage{PromptTokens: 1, CompletionTokens: 512, TotalTokens: 513},
	}
}

func TestValidateOutputUsesIndependentExplicitByteLimit(t *testing.T) {
	content := strings.Repeat("á", 1144) // 2,288 UTF-8 bytes, 512 reported tokens.
	if err := validateOutput(outputPolicy(512, 2288, true), textResponse, outputResponse(content)); err != nil {
		t.Fatalf("explicit byte limit rejected valid output: %v", err)
	}
	err := validateOutput(outputPolicy(512, 2048, true), textResponse, outputResponse(content))
	var outputErr *outputValidationError
	if !errors.As(err, &outputErr) || outputErr.Code != "output_bytes_exceeded" {
		t.Fatalf("error = %v, want output_bytes_exceeded", err)
	}
}

func TestValidateOutputExplicitBytesAreNotTokenDerived(t *testing.T) {
	content := strings.Repeat("x", 4097) // above the tokens*4 fallback (1024*4), below 16384.
	if err := validateOutput(outputPolicy(1024, 16384, true), textResponse, outputResponse(content)); err != nil {
		t.Fatalf("explicit 16384 limit rejected output that legacy fallback would fail: %v", err)
	}
	err := validateOutput(outputPolicy(1024, 16384, false), textResponse, outputResponse(content))
	var outputErr *outputValidationError
	if !errors.As(err, &outputErr) || outputErr.Code != "output_bytes_exceeded" {
		t.Fatalf("error = %v, want legacy 4096-byte fallback rejection", err)
	}
}

func TestValidateOutputCountsUTF8BytesAtBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		content  string
		maxBytes int
		wantErr  bool
	}{
		{name: "n-minus-one", content: "á" + strings.Repeat("x", 2), maxBytes: 4, wantErr: false},
		{name: "n", content: "á" + strings.Repeat("x", 3), maxBytes: 5, wantErr: false},
		{name: "n-plus-one", content: "á" + strings.Repeat("x", 4), maxBytes: 5, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateOutput(outputPolicy(1, test.maxBytes, true), textResponse, outputResponse(test.content))
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, want error=%v", err, test.wantErr)
			}
		})
	}
}

func TestValidateOutputRetainsLegacyTokenByteFallback(t *testing.T) {
	content := strings.Repeat("x", 2049)
	err := validateOutput(outputPolicy(512, 16384, false), textResponse, outputResponse(content))
	var outputErr *outputValidationError
	if !errors.As(err, &outputErr) || outputErr.Code != "output_bytes_exceeded" {
		t.Fatalf("error = %v, want legacy output_bytes_exceeded", err)
	}
}
