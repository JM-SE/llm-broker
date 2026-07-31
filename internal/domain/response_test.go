package domain

import (
	"bytes"
	"strings"
	"testing"
)

func TestSchemaContractCompilesDraft2020(t *testing.T) {
	schema := []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"title": {"type": "string"},
			"tags": {"type": "array", "items": {"type": "string"}}
		},
		"required": ["title"],
		"additionalProperties": false
	}`)
	sc, err := NewSchemaContract(schema)
	if err != nil {
		t.Fatalf("NewSchemaContract: %v", err)
	}
	if !bytes.Contains(sc.Raw(), []byte(`"type"`)) {
		t.Error("raw schema lost")
	}
}

func TestSchemaContractRejectsRemoteRefs(t *testing.T) {
	cases := []string{
		`{"$ref": "https://evil.example/schema.json"}`,
		`{"$ref": "http://evil.example/schema.json"}`,
		`{"$ref": "//evil.example/schema.json"}`,
		`{"$ref": "file:///etc/passwd"}`,
		`{"$ref": "data:text/plain,hi"}`,
		`{"$id": "https://evil.example/schema.json", "type": "object"}`,
	}
	for _, raw := range cases {
		if _, err := NewSchemaContract([]byte(raw)); err == nil {
			t.Errorf("remote ref should be rejected: %s", raw)
		}
	}
}

func TestSchemaContractAllowsLocalRefs(t *testing.T) {
	raw := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {"a": {"$ref": "#/$defs/x"}},
		"$defs": {"x": {"type": "string"}}
	}`
	if _, err := NewSchemaContract([]byte(raw)); err != nil {
		t.Fatalf("local ref rejected: %v", err)
	}
}

func TestSchemaContractRejectsUnsupportedDraft(t *testing.T) {
	raw := `{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object"}`
	if _, err := NewSchemaContract([]byte(raw)); err == nil {
		t.Error("draft-07 should be rejected")
	}
}

func TestSchemaContractBounds(t *testing.T) {
	// Size bound.
	big := []byte(`{"type": "object", "description": "` + strings.Repeat("x", MaxSchemaBytes+1) + `"}`)
	if _, err := NewSchemaContract(big); err == nil {
		t.Error("oversized schema should be rejected")
	}
	// Depth bound: nest objects one deeper than allowed.
	deep := strings.Repeat(`{"nested": `, MaxSchemaDepth+1) + `1` + strings.Repeat(`}`, MaxSchemaDepth+1)
	if _, err := NewSchemaContract([]byte(deep)); err == nil {
		t.Error("over-deep schema should be rejected")
	}
	// Property-count bound: one object declaring more than allowed.
	var pb strings.Builder
	pb.WriteString(`{"type": "object", "properties": {`)
	for i := 0; i < MaxSchemaProperties+1; i++ {
		if i > 0 {
			pb.WriteString(",")
		}
		// Unique key per entry: i%10-style repetition would collapse to
		// duplicate JSON keys and undercount the properties map.
		pb.WriteString(`"p` + strings.Repeat("x", i+1) + `": {"type": "string"}`)
	}
	pb.WriteString("}}")
	if _, err := NewSchemaContract([]byte(pb.String())); err == nil {
		t.Error("over-property schema should be rejected")
	}
}

func TestSchemaContractCanonicalKeyOrder(t *testing.T) {
	a := []byte(`{"type": "object", "properties": {"b": {"type": "string"}, "a": {"type": "string"}}}`)
	b := []byte(`{"properties": {"a": {"type": "string"}, "b": {"type": "string"}}, "type": "object"}`)
	sa, err := NewSchemaContract(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := NewSchemaContract(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sa.Canonical(), sb.Canonical()) {
		t.Errorf("canonical differs:\n%s\n%s", sa.Canonical(), sb.Canonical())
	}
}

func TestResponseContractKindRules(t *testing.T) {
	if err := (ResponseContract{Kind: ResponseText}).Validate(); err != nil {
		t.Errorf("text without schema: %v", err)
	}
	if err := (ResponseContract{Kind: ResponseJSONSchema}).Validate(); err == nil {
		t.Error("json_schema without schema must fail")
	}
	sc, err := NewSchemaContract([]byte(`{"type": "object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (ResponseContract{Kind: ResponseJSONSchema, Schema: sc}).Validate(); err != nil {
		t.Errorf("json_schema with schema: %v", err)
	}
	if err := (ResponseContract{Kind: ResponseText, Schema: sc}).Validate(); err == nil {
		t.Error("text with schema must fail")
	}
	if err := (ResponseContract{Kind: "xml"}).Validate(); err == nil {
		t.Error("unknown kind must fail")
	}
}
