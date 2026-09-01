package domain

import (
	"bytes"
	"strings"
	"testing"
)

func validSubmission() *TaskSubmission {
	return &TaskSubmission{
		Workload:   "default",
		Capability: "instruct",
		Messages: []Message{
			{Role: RoleSystem, Content: "Be concise."},
			{Role: RoleUser, Content: "Summarize the report."},
		},
		Generation: Generation{MaxTokens: int64Ptr(512), Temperature: float64Ptr(0.7)},
		Response:   ResponseContract{Kind: ResponseText},
		Trace:      Trace{"job_id": "42"},
	}
}

func TestDecodeTaskSubmission(t *testing.T) {
	body := `{
		"workload": "default",
		"capability": "instruct",
		"messages": [
			{"role": "user", "content": "hi"}
		],
		"generation": {"max_tokens": 128},
		"response": {"kind": "text"}
	}`
	s, err := DecodeTaskSubmission(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Workload != "default" || s.Response.Kind != ResponseText {
		t.Errorf("unexpected submission: %+v", s)
	}
}

func TestDecodeTaskSubmissionWithJSONSchema(t *testing.T) {
	body := `{
		"workload": "default",
		"capability": "instruct",
		"messages": [{"role": "user", "content": "return an object"}],
		"response": {
			"kind": "json_schema",
			"schema": {"type": "object", "required": ["answer"], "properties": {"answer": {"type": "string"}}}
		}
	}`
	s, err := DecodeTaskSubmission(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode JSON Schema submission: %v", err)
	}
	if s.Response.Schema == nil {
		t.Fatal("decoded JSON Schema is nil")
	}
	if err := s.Response.Schema.Validate([]byte(`{"answer":"ok"}`)); err != nil {
		t.Fatalf("valid structured output rejected: %v", err)
	}
	if err := s.Response.Schema.Validate([]byte(`{"wrong":true}`)); err == nil {
		t.Fatal("invalid structured output unexpectedly accepted")
	}
}

func TestDecodeTaskSubmissionRejectsUnknownFields(t *testing.T) {
	bodies := []string{
		`{"workload": "default", "capability": "instruct", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "text"}, "model": "gpt-4"}`,
		`{"workload": "default", "capability": "instruct", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "text"}, "generation": {"frequency_penalty": 1}}`,
		`{"workload": "default", "capability": "instruct", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "text"}, "priority": 999}`,
		`{"workload": "default", "capability": "instruct", "messages": [{"role": "tool", "content": "x"}], "response": {"kind": "text"}}`,
		`{"workload": "default", "capability": "instruct", "messages": [{"role": "user", "content": "x"}], "response": {"kind": "text"}, "trace": {"job": {"nested": 1}}}`,
	}
	for _, b := range bodies {
		if _, err := DecodeTaskSubmission(strings.NewReader(b)); err == nil {
			t.Errorf("should reject: %s", b)
		}
	}
}

func TestSubmissionValidateLimits(t *testing.T) {
	// Message count.
	s := validSubmission()
	for i := 0; i < MaxMessages; i++ {
		s.Messages = append(s.Messages, Message{Role: RoleUser, Content: "m"})
	}
	if err := s.Validate(); err == nil {
		t.Error("too many messages must fail")
	}
	// Combined size.
	s = validSubmission()
	s.Messages = []Message{{Role: RoleUser, Content: strings.Repeat("x", MaxCombinedMessageBytes+1)}}
	if err := s.Validate(); err == nil {
		t.Error("oversized combined content must fail")
	}
	// Empty messages.
	s = validSubmission()
	s.Messages = nil
	if err := s.Validate(); err == nil {
		t.Error("empty messages must fail")
	}
	// Trace cardinality.
	s = validSubmission()
	tr := Trace{}
	for i := 0; i <= MaxTraceKeys; i++ {
		tr[string(rune('a'+i))] = "v"
	}
	s.Trace = tr
	if err := s.Validate(); err == nil {
		t.Error("oversized trace must fail")
	}
	// Trace value size.
	s = validSubmission()
	s.Trace = Trace{"k": strings.Repeat("x", MaxTraceValueBytes+1)}
	if err := s.Validate(); err == nil {
		t.Error("oversized trace value must fail")
	}
}

func TestSubmissionCanonicalBytesEquivalence(t *testing.T) {
	a := `{
		"workload": "default",
		"capability": "instruct",
		"messages": [
			{"role": "system", "content": "s"},
			{"role": "user", "content": "u"}
		],
		"generation": {"max_tokens": 512, "temperature": 0.7, "stop": ["B", "A"]},
		"response": {"kind": "text"},
		"trace": {"job_id": "42"}
	}`
	b := `{
		"capability": "instruct",
		"trace": {"job_id": "42"},
		"response": {"kind": "text"},
		"generation": {"stop": ["A", "B"], "temperature": 0.7, "max_tokens": 512},
		"messages": [
			{"content": "s", "role": "system"},
			{"content": "u", "role": "user"}
		],
		"workload": "default"
	}`
	sa, err := DecodeTaskSubmission(strings.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	sb, err := DecodeTaskSubmission(strings.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := sa.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	cb, err := sb.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca, cb) {
		t.Errorf("canonical bytes differ:\n%s\n%s", ca, cb)
	}
	ha, err := sa.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	hb, err := sb.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("request hashes differ: %s vs %s", ha, hb)
	}
	// Different content must produce different hash.
	sb.Messages[1].Content = "different"
	hb2, _ := sb.RequestHash()
	if ha == hb2 {
		t.Error("different content must produce different hash")
	}
}

func TestResultValidate(t *testing.T) {
	if err := (Result{Content: "x", FinishReason: FinishStop, Usage: Usage{1, 2, 3}}).Validate(); err != nil {
		t.Errorf("valid result rejected: %v", err)
	}
	if err := (Result{FinishReason: "weird"}).Validate(); err == nil {
		t.Error("invalid finish reason must fail")
	}
	if err := (Result{FinishReason: FinishStop, Usage: Usage{-1, 0, 0}}).Validate(); err == nil {
		t.Error("negative usage must fail")
	}
}

func TestTraceValidate(t *testing.T) {
	if err := (Trace{}).Validate(); err != nil {
		t.Error("empty trace must pass")
	}
	if err := (Trace{"job_id": "42"}).Validate(); err != nil {
		t.Errorf("simple trace rejected: %v", err)
	}
	if err := (Trace{"Bad Key": "v"}).Validate(); err == nil {
		t.Error("invalid trace key must fail")
	}
}
