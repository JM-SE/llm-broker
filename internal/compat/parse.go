package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// These limits are part of consumer-compat/v1. They bound parsing work before
// any semantic decoder is invoked.
const (
	MaxManifestBytes      = 64 * 1024
	MaxManifestDepth      = 8
	MaxManifestProperties = 32
	MaxManifestItems      = 16
	MaxManifestString     = 256
)

// Parse reads and strictly validates one untrusted manifest.
func Parse(r io.Reader) (Manifest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest could not be read")
	}
	if len(raw) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("manifest exceeds the size limit")
	}
	if err := inspectJSON(raw); err != nil {
		return Manifest{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Manifest{}, fmt.Errorf("manifest must be a JSON object")
	}
	allowed := map[string]bool{
		"version": true, "api_range": true, "workload": true, "capability": true,
		"generation": true, "response": true, "max_request_content_bytes": true,
		"required_min_output_bytes": true, "min_workload_timeout_seconds": true,
		"min_backend_response_start_timeout_seconds": true, "max_acceptable_attempts": true,
		"consumer_overall_deadline_seconds": true,
	}
	for key := range fields {
		if !allowed[key] {
			return Manifest{}, fieldError(key, "is not allowed")
		}
	}
	for _, key := range []string{"version", "api_range", "workload", "capability", "generation", "response", "max_request_content_bytes", "required_min_output_bytes", "min_workload_timeout_seconds", "min_backend_response_start_timeout_seconds", "max_acceptable_attempts", "consumer_overall_deadline_seconds"} {
		rawValue, ok := fields[key]
		if !ok {
			return Manifest{}, fieldError(key, "is required")
		}
		if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
			return Manifest{}, fieldError(key, "must not be null")
		}
	}

	var m Manifest
	if err := json.Unmarshal(fields["version"], &m.Version); err != nil {
		return Manifest{}, fieldError("version", "has an invalid type")
	}
	if err := json.Unmarshal(fields["api_range"], &m.APIRange); err != nil {
		return Manifest{}, fieldError("api_range", "has an invalid type")
	}
	m.APIRange = normalizeRange(m.APIRange)
	if err := json.Unmarshal(fields["workload"], &m.Workload); err != nil {
		return Manifest{}, fieldError("workload", "has an invalid type")
	}
	if err := json.Unmarshal(fields["capability"], &m.Capability); err != nil {
		return Manifest{}, fieldError("capability", "has an invalid type")
	}
	if err := json.Unmarshal(fields["generation"], &m.Generation); err != nil {
		return Manifest{}, fieldError("generation", "is invalid")
	}
	var responseFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["response"], &responseFields); err != nil || responseFields == nil {
		return Manifest{}, fieldError("response", "must be an object")
	}
	for key := range responseFields {
		if key != "kind" && key != "schema_required" {
			return Manifest{}, fieldError("response."+key, "is not allowed")
		}
	}
	kind, ok := responseFields["kind"]
	if !ok || bytes.Equal(bytes.TrimSpace(kind), []byte("null")) {
		return Manifest{}, fieldError("response.kind", "is required")
	}
	if err := json.Unmarshal(kind, &m.Response.Kind); err != nil {
		return Manifest{}, fieldError("response.kind", "has an invalid type")
	}
	if schema, ok := responseFields["schema_required"]; ok {
		if bytes.Equal(bytes.TrimSpace(schema), []byte("null")) || json.Unmarshal(schema, &m.Response.SchemaRequired) != nil {
			return Manifest{}, fieldError("response.schema_required", "must be a boolean")
		}
		m.Response.schemaSet = true
	}
	for _, numeric := range []struct {
		name   string
		target *int
	}{
		{"max_request_content_bytes", &m.MaxRequestContentBytes},
		{"required_min_output_bytes", &m.RequiredMinOutputBytes},
		{"min_workload_timeout_seconds", &m.MinWorkloadTimeoutSeconds},
		{"min_backend_response_start_timeout_seconds", &m.MinBackendResponseStartTimeoutSeconds},
		{"max_acceptable_attempts", &m.MaxAcceptableAttempts},
		{"consumer_overall_deadline_seconds", &m.ConsumerOverallDeadlineSeconds},
	} {
		if err := json.Unmarshal(fields[numeric.name], numeric.target); err != nil {
			return Manifest{}, fieldError(numeric.name, "has an invalid type")
		}
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	// Return the normalized value, not just a digest normalized projection.
	m.Generation.Stop = m.Generation.NormalizedStop()
	return m, nil
}

// ParseFile is the filesystem boundary used by the CLI.
func ParseFile(path string) (Manifest, error) {
	f, err := openManifest(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest could not be opened")
	}
	defer f.Close()
	return Parse(f)
}

// openManifest is a variable so parser tests can remain independent of a
// particular filesystem implementation.
var openManifest = func(path string) (io.ReadCloser, error) { return osOpen(path) }

func inspectJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := walkValue(dec, 0); err != nil {
		return fmt.Errorf("manifest JSON is invalid")
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("manifest has trailing content")
	}
	return nil
}

func walkValue(dec *json.Decoder, depth int) error {
	if depth > MaxManifestDepth {
		return fmt.Errorf("manifest nesting exceeds the limit")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if s, ok := tok.(string); ok && len(s) > MaxManifestString {
		return fmt.Errorf("manifest string exceeds the limit")
	} else if ok {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		count := 0
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("object key is invalid")
			}
			if len(name) > MaxManifestString {
				return fmt.Errorf("manifest property exceeds the limit")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("manifest contains duplicate properties")
			}
			seen[name] = struct{}{}
			count++
			if count > MaxManifestProperties {
				return fmt.Errorf("manifest has too many properties")
			}
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		count := 0
		for dec.More() {
			count++
			if count > MaxManifestItems {
				return fmt.Errorf("manifest array has too many items")
			}
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("manifest JSON delimiter is invalid")
	}
}

type semver struct{ major, minor, patch int }

type comparator struct {
	op string
	v  semver
}

func parseRange(raw string) ([]comparator, error) {
	raw = normalizeRange(raw)
	if raw == "" || strings.ContainsAny(raw, "*xX|") {
		return nil, fmt.Errorf("invalid range")
	}
	parts := strings.Fields(raw)
	result := make([]comparator, 0, len(parts))
	for _, part := range parts {
		op := "="
		for _, candidate := range []string{"<=", ">=", "<", ">", "="} {
			if strings.HasPrefix(part, candidate) {
				op = candidate
				part = strings.TrimPrefix(part, candidate)
				break
			}
		}
		v, err := parseSemver(part)
		if err != nil {
			return nil, err
		}
		result = append(result, comparator{op: op, v: v})
	}
	return result, nil
}

func parseSemver(raw string) (semver, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("invalid version")
	}
	values := [3]int{}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semver{}, fmt.Errorf("invalid version")
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return semver{}, fmt.Errorf("invalid version")
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > maxSemverPart {
			return semver{}, fmt.Errorf("invalid version")
		}
		values[i] = value
	}
	return semver{values[0], values[1], values[2]}, nil
}

func compareVersion(left, right semver) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func rangeContains(raw, current string) bool {
	comparators, err := parseRange(raw)
	if err != nil {
		return false
	}
	version, err := parseSemver(current)
	if err != nil {
		return false
	}
	for _, item := range comparators {
		cmp := compareVersion(version, item.v)
		switch item.op {
		case "=":
			if cmp != 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case ">=":
			if cmp < 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		}
	}
	return true
}

// Keep os.File construction behind a tiny helper so the parser's imports and
// errors remain intentionally quiet at the untrusted boundary.
func osOpen(path string) (io.ReadCloser, error) { return os.Open(path) }
