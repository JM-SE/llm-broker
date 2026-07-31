package domain

import (
	"strings"
	"testing"
)

// FuzzDecodeTaskSubmission ensures the strict decoder never panics on
// arbitrary input and only ever accepts fully valid submissions.
func FuzzDecodeTaskSubmission(f *testing.F) {
	seeds := []string{
		`{"workload": "w", "capability": "c", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "text"}}`,
		`{}`,
		`{"workload": "w"}`,
		`not json`,
		`{"workload": "w", "capability": "c", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "json_schema", "schema": {"type": "object"}}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		s, err := DecodeTaskSubmission(strings.NewReader(data))
		if err != nil {
			return
		}
		// Whatever was accepted must be intrinsically valid.
		if err := s.Validate(); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
		if _, err := s.CanonicalBytes(); err != nil {
			t.Fatalf("accepted but not canonicalizable: %v", err)
		}
	})
}

// FuzzNewSchemaContract ensures schema construction never panics and
// accepted schemas are always canonicalizable.
func FuzzNewSchemaContract(f *testing.F) {
	seeds := []string{
		`{"type": "object"}`,
		`{"$ref": "https://evil.example/s"}`,
		`{"$schema": "http://json-schema.org/draft-07/schema#", "type": "string"}`,
		`[]`,
		`"str"`,
		`42`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		sc, err := NewSchemaContract([]byte(data))
		if err != nil {
			return
		}
		if len(sc.Canonical()) == 0 {
			t.Fatal("empty canonical output")
		}
	})
}
