// Package tests hosts product-level integration checks that span packages,
// including OpenAPI contract validation.
package tests

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const openapiPath = "../openapi.yaml"

// TestOpenAPIValidates loads the client-facing OpenAPI document, resolves
// every reference, and runs structural validation.
func TestOpenAPIValidates(t *testing.T) {
	data, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Info.Version == "" || doc.Info.Title == "" {
		t.Error("missing info")
	}
}

// TestOpenAPIEndpoints pins the client-facing surface of the product.
func TestOpenAPIEndpoints(t *testing.T) {
	data, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/health", "/ready", "/metrics",
		"/v1/tasks", "/v1/tasks/{task_id}",
		"/v1/tasks/{task_id}/result", "/v1/tasks/{task_id}/cancel",
	} {
		if doc.Paths.Find(path) == nil {
			t.Errorf("missing endpoint %s", path)
		}
	}
	// Exactly the client surface, no admin or provider endpoints.
	if want := len(doc.Paths.Map()); want != 7 {
		t.Errorf("path count = %d, want 7 (public v1 surface)", want)
	}
}

// TestOpenAPINoProviderSelection pins the invariant that no client-facing
// schema carries backend or model selection fields.
func TestOpenAPINoProviderSelection(t *testing.T) {
	data, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	s := doc.Components.Schemas
	for _, name := range []string{"TaskSubmission", "Generation", "ResponseContract", "Task"} {
		if _, ok := s[name]; !ok {
			t.Errorf("missing schema %s", name)
		}
	}
	// No client-visible schema may carry selection or identity fields.
	banned := []string{"model", "base_url", "backend", "priority", "credential", "client_id"}
	for _, name := range []string{"TaskSubmission", "Generation", "ResponseContract", "Task", "Message", "TraceMetadata"} {
		props := s[name].Value.Properties
		for _, b := range banned {
			if _, ok := props[b]; ok {
				t.Errorf("schema %s must not expose field %q", name, b)
			}
		}
	}
}

// TestOpenAPIWaitContract pins the bounded synchronous wait: POST /v1/tasks
// must declare 200 (terminal within window) alongside 201 and 202.
func TestOpenAPIWaitContract(t *testing.T) {
	data, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	post := doc.Paths.Find("/v1/tasks").Post
	if post == nil {
		t.Fatal("missing POST /v1/tasks")
	}
	for _, status := range []string{"200", "201", "202"} {
		code, err := strconv.Atoi(status)
		if err != nil {
			t.Fatal(err)
		}
		if post.Responses.Status(code) == nil {
			t.Errorf("POST /v1/tasks missing response %s", status)
		}
	}
}

// TestOpenAPIExampleSubmissionsValidates runs representative example
// request bodies through the OpenAPI schemas.
func TestOpenAPIExampleSubmissionsValidates(t *testing.T) {
	data, err := os.ReadFile(openapiPath)
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	subSchema := doc.Components.Schemas["TaskSubmission"].Value
	examples := []string{
		`{"workload": "default", "capability": "instruct",
		  "messages": [{"role": "user", "content": "Hello"}],
		  "generation": {"max_tokens": 128, "temperature": 0.7},
		  "response": {"kind": "text"},
		  "trace": {"job_id": "42"}}`,
		`{"workload": "default", "capability": "instruct",
		  "messages": [{"role": "system", "content": "x"}, {"role": "assistant", "content": "y"}],
		  "response": {"kind": "json_schema", "schema": {"type": "object"}}}`,
	}
	for i, ex := range examples {
		var msg any
		if err := json.Unmarshal([]byte(ex), &msg); err != nil {
			t.Fatalf("example %d: %v", i, err)
		}
		if err := subSchema.VisitJSON(msg); err != nil {
			t.Errorf("example %d invalid against schema: %v", i, err)
		}
	}
	// The kind/schema rule is expressed structurally via if/then/else
	// (kin-openapi does not evaluate if/then/else against instances; the
	// behavioral check belongs to the API layer in P04). Pin the shape:
	// when kind is json_schema the schema property is required, otherwise
	// it must not be present.
	rcSchema := doc.Components.Schemas["ResponseContract"].Value
	if rcSchema.If == nil || rcSchema.Then == nil || rcSchema.Else == nil {
		t.Fatal("ResponseContract must declare if/then/else for kind/schema rules")
	}
	if len(rcSchema.Then.Value.Required) == 0 {
		t.Error("then branch must require the schema property")
	}
	if len(rcSchema.Else.Value.Required) != 0 {
		t.Error("else branch must not require the schema property")
	}
}
