package policy

import (
	"encoding/json"
	"testing"

	"github.com/JM-SE/llm-broker/internal/domain"
)

func TestValidateGenerationDenyByAbsenceIncludesEmptyAndNull(t *testing.T) {
	cases := []string{
		`{"stop":[]}`,
		`{"stop":null}`,
		`{"temperature":null}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var generation domain.Generation
			if err := json.Unmarshal([]byte(raw), &generation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateGeneration(generation, []byte(`{}`), 2048); err == nil {
				t.Fatalf("%s bypassed deny-by-absence", raw)
			}
		})
	}
}

func TestValidateGenerationChecksAllowedValues(t *testing.T) {
	var generation domain.Generation
	if err := json.Unmarshal([]byte(`{"stop":[],"max_tokens":64}`), &generation); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGeneration(generation, []byte(`{"stop":{"max_items":1},"max_tokens":{"max":128}}`), 256); err != nil {
		t.Fatalf("allowed generation rejected: %v", err)
	}

	var tooMany domain.Generation
	if err := json.Unmarshal([]byte(`{"stop":["a","b"]}`), &tooMany); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGeneration(tooMany, []byte(`{"stop":{"max_items":1}}`), 256); err == nil {
		t.Fatal("stop max_items was not enforced")
	}
}
