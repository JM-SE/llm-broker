package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ResponseKind is the stable response-contract vocabulary.
type ResponseKind string

const (
	// ResponseText asks for plain text output (the default).
	ResponseText ResponseKind = "text"
	// ResponseJSONObject asks for an object-shaped JSON output without
	// requiring a client schema.
	ResponseJSONObject ResponseKind = "json_object"
	// ResponseJSONSchema asks for output validated against a
	// client-provided Draft 2020-12 schema.
	ResponseJSONSchema ResponseKind = "json_schema"
)

var validResponseKinds = map[ResponseKind]bool{
	ResponseText:       true,
	ResponseJSONObject: true,
	ResponseJSONSchema: true,
}

// Valid reports whether the kind is part of the stable vocabulary.
func (k ResponseKind) Valid() bool { return validResponseKinds[k] }

// SchemaContract is a client-provided JSON Schema treated strictly as data.
// It is bounded in size, depth, and declared properties; remote references
// are rejected; compilation cannot perform network or filesystem access.
type SchemaContract struct {
	raw []byte // original raw bytes, canonicalized on construction
}

// NewSchemaContract validates and compiles raw JSON Schema bytes.
//
// Safety guarantees:
//   - Draft 2020-12 only; other $schema dialects are rejected.
//   - Size <= MaxSchemaBytes, depth <= MaxSchemaDepth, and every object
//     declares at most MaxSchemaProperties properties.
//   - "$ref" and "$id" values must not carry a URI scheme or fragment-only
//     remote form; the compiler uses an empty scheme loader, so even a
//     syntactically remote reference cannot trigger network or filesystem
//     access.
func NewSchemaContract(raw []byte) (*SchemaContract, error) {
	canonical, err := canonicalizeSchemaJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := compileSchema(canonical); err != nil {
		return nil, err
	}
	return &SchemaContract{raw: canonical}, nil
}

// Raw returns the canonicalized schema bytes.
func (s *SchemaContract) Raw() []byte { return s.raw }

// Canonical returns the deterministic bytes of the schema.
func (s *SchemaContract) Canonical() []byte { return s.raw }

// Validate checks an instance document (JSON bytes) against the compiled
// schema. Compilation is fully local: no network or filesystem access is
// possible. Used by the worker before persisting a structured result.
func (s *SchemaContract) Validate(instance []byte) error {
	if len(instance) == 0 {
		return fmt.Errorf("output is empty")
	}
	compiler := newSchemaCompiler()
	doc, err := decodeSchemaDoc(s.raw)
	if err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	if err := compiler.AddResource("task-schema.json", doc); err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	sch, err := compiler.Compile("task-schema.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(instance))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("output is not valid JSON: %w", err)
	}
	if err := sch.Validate(v); err != nil {
		return fmt.Errorf("output does not satisfy the response schema: %w", err)
	}
	return nil
}

// canonicalizeSchemaJSON parses the schema, verifies structural bounds and
// remote-reference rejection, and re-encodes it with deterministic key
// ordering so semantically identical schemas share bytes.
func canonicalizeSchemaJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("schema is empty")
	}
	if len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("schema size %d exceeds %d bytes", len(raw), MaxSchemaBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	if err := checkSchemaTree(v, 0); err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-encode schema: %w", err)
	}
	return out, nil
}

// checkSchemaTree enforces depth, property-count, and remote-reference
// bounds over the decoded schema.
func checkSchemaTree(v any, depth int) error {
	if depth > MaxSchemaDepth {
		return fmt.Errorf("schema nesting exceeds depth %d", MaxSchemaDepth)
	}
	switch node := v.(type) {
	case map[string]any:
		if len(node) > MaxSchemaProperties {
			return fmt.Errorf("schema object declares %d properties, max %d", len(node), MaxSchemaProperties)
		}
		if ref, ok := node["$ref"]; ok {
			if err := rejectRemoteURI("$ref", ref); err != nil {
				return err
			}
		}
		if id, ok := node["$id"]; ok {
			if err := rejectRemoteURI("$id", id); err != nil {
				return err
			}
		}
		if sch, ok := node["$schema"].(string); ok && sch != "" {
			if !strings.HasPrefix(sch, "https://json-schema.org/draft/2020-12/") &&
				!strings.HasPrefix(sch, "http://json-schema.org/draft/2020-12/") {
				return fmt.Errorf("unsupported $schema %q (only Draft 2020-12)", sch)
			}
		}
		for _, child := range node {
			if err := checkSchemaTree(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(node) > MaxSchemaProperties {
			return fmt.Errorf("schema array has %d items, max %d", len(node), MaxSchemaProperties)
		}
		for _, child := range node {
			if err := checkSchemaTree(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectRemoteURI rejects URI values that could reference external
// resources. Local fragments ("#/definitions/x") and plain names are
// allowed; anything with a scheme or a non-empty opaque form is not.
func rejectRemoteURI(keyword string, v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("schema %s must be a string", keyword)
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "//") {
		return fmt.Errorf("schema %s %q is not allowed (remote references are rejected)", keyword, s)
	}
	if strings.HasPrefix(s, "file:") || strings.HasPrefix(s, "data:") {
		return fmt.Errorf("schema %s %q is not allowed (non-HTTP schemes are rejected)", keyword, s)
	}
	return nil
}

// compileSchema compiles the schema with a compiler that cannot load any
// external resource. Any reference not registered locally fails to compile.
func compileSchema(raw []byte) error {
	compiler := newSchemaCompiler()
	doc, err := decodeSchemaDoc(raw)
	if err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	if err := compiler.AddResource("task-schema.json", doc); err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	if _, err := compiler.Compile("task-schema.json"); err != nil {
		return fmt.Errorf("invalid JSON Schema: %w", err)
	}
	return nil
}

// ResponseContract selects the output shape of an inference.
type ResponseContract struct {
	Kind   ResponseKind    `json:"kind"`
	Schema *SchemaContract `json:"schema,omitempty"`
}

// Validate checks the kind and schema presence rules.
func (r ResponseContract) Validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("unsupported response kind %q (want text, json_object, or json_schema)", r.Kind)
	}
	if r.Kind == ResponseJSONSchema && r.Schema == nil {
		return fmt.Errorf("response kind %q requires a schema", r.Kind)
	}
	if r.Kind != ResponseJSONSchema && r.Schema != nil {
		return fmt.Errorf("response kind %q must not carry a schema", r.Kind)
	}
	return nil
}

// Canonical renders deterministic JSON bytes of the response contract.
func (r *ResponseContract) Canonical() ([]byte, error) {
	canon := struct {
		Kind   ResponseKind `json:"kind"`
		Schema []byte       `json:"schema,omitempty"`
	}{Kind: r.Kind}
	if r.Schema != nil {
		canon.Schema = r.Schema.Canonical()
	}
	return json.Marshal(&canon)
}
