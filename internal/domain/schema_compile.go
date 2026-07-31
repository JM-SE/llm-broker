package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// newSchemaCompiler returns a Draft 2020-12 compiler that cannot load any
// external resource: the scheme loader is empty, so every unresolvable
// reference fails at compile time without network or filesystem access.
func newSchemaCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{}) // no scheme can be loaded
	return c
}

// decodeSchemaDoc decodes raw JSON into the decoded value form the
// jsonschema compiler expects (AddResource takes an already-decoded value,
// not a reader). json.Number preserves precision exactly like the library's
// own UnmarshalJSON.
func decodeSchemaDoc(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid content after top-level value")
	}
	return doc, nil
}

// CompileSchema compiles raw Draft 2020-12 schema bytes against the same
// offline compiler used during construction. Callers that already hold a
// *SchemaContract do not need this; it exists for store-level validation.
func CompileSchema(raw []byte) (*jsonschema.Schema, error) {
	compiler := newSchemaCompiler()
	doc, err := decodeSchemaDoc(raw)
	if err != nil {
		return nil, err
	}
	if err := compiler.AddResource("task-schema.json", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("task-schema.json")
}
