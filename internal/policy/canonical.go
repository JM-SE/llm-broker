// Package policy contains the pure, dependency-light projections shared by
// configuration, provisioning, and runtime fencing.  It deliberately has no
// database, HTTP, adapter, or command-line dependencies.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	TopologyDomain = "llm-broker/topology/v1"
	GlobalApplyID  = "llm-broker/policy-apply/global"

	clientPrefix     byte = 0x11
	clientNamePrefix byte = 0x12
	policyPrefix     byte = 0x21
	grantPrefix      byte = 0x31
	globalPrefix     byte = 0x41
)

// advisoryKeyDigest is package-local so the policy package's own tests can
// force a same-domain collision without exposing a production mutation seam.
var advisoryKeyDigest = func(identity string) [32]byte {
	return sha256.Sum256([]byte(identity))
}

// Backend describes the routing facts which are safe and relevant to the
// topology digest. Credential values are intentionally not represented.
type Backend struct {
	Type     string
	BaseURL  string
	Model    string
	Features []string
}

// Topology is the process-local capability/backend routing topology.
type Topology struct {
	Capabilities map[string]string
	Backends     map[string]Backend
}

// Digest computes the secret-free, canonical topology digest. Mutable
// workload policy is not an input to this function.
func Digest(t Topology) [32]byte {
	backends := make(map[string]canonicalBackend, len(t.Backends))
	for name, b := range t.Backends {
		features := append([]string(nil), b.Features...)
		sort.Strings(features)
		backends[name] = canonicalBackend{Type: b.Type, BaseURL: b.BaseURL, Model: b.Model, Features: features}
	}
	payload := struct {
		Domain       string                      `json:"domain"`
		Capabilities map[string]string           `json:"capabilities"`
		Backends     map[string]canonicalBackend `json:"backends"`
	}{TopologyDomain, t.Capabilities, backends}
	b, _ := json.Marshal(payload)
	return sha256.Sum256(b)
}

type canonicalBackend struct {
	Type     string   `json:"type"`
	BaseURL  string   `json:"base_url,omitempty"`
	Model    string   `json:"model,omitempty"`
	Features []string `json:"features,omitempty"`
}

// CanonicalSet rejects duplicates before sorting and returns a defensive,
// lexical projection. Empty is a valid set and means deny-all where used for
// capability authorization.
func CanonicalSet(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for i, value := range values {
		if value == "" || value == "*" || strings.ContainsRune(value, '\x00') || !utf8.ValidString(value) {
			return nil, fmt.Errorf("%s[%d] is not a valid canonical value", field, i)
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("%s contains duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

// CanonicalGeneration renders JSON with stable object-key ordering. The
// caller remains responsible for validating the field vocabulary and bounds.
func CanonicalGeneration(value any) ([]byte, error) {
	return CanonicalJSONValue(value, "generation")
}

// CanonicalJSON normalizes arbitrary JSON data using the same representation
// used by PostgreSQL jsonb comparisons. It is used for semantic policy
// equality, so whitespace and object-key ordering cannot cause policy drift.
func CanonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("canonicalize JSON: trailing content")
	} else if err != io.EOF {
		return nil, fmt.Errorf("canonicalize JSON: trailing content: %w", err)
	}
	return CanonicalJSONValue(value, "JSON")
}

func CanonicalJSONValue(value any, field string) ([]byte, error) {
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s: %w", field, err)
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s: %w", field, err)
	}
	return bytes.TrimSpace(b), nil
}

func JSONSemanticallyEqual(a, b []byte) bool {
	va, errA := decodeJSON(a)
	vb, errB := decodeJSON(b)
	if errA != nil || errB != nil {
		return false
	}
	return jsonValuesEqual(va, vb)
}

func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON")
		}
		return nil, fmt.Errorf("trailing JSON: %w", err)
	}
	return value, nil
}

func jsonValuesEqual(a, b any) bool {
	switch left := a.(type) {
	case nil:
		return b == nil
	case bool:
		right, ok := b.(bool)
		return ok && left == right
	case string:
		right, ok := b.(string)
		return ok && left == right
	case json.Number:
		right, ok := b.(json.Number)
		if !ok {
			return false
		}
		return jsonNumbersEqual(left, right)
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !jsonValuesEqual(left[i], right[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !jsonValuesEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func jsonNumbersEqual(a, b json.Number) bool {
	left, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return false
	}
	right, ok := new(big.Rat).SetString(string(b))
	if !ok {
		return false
	}
	return left.Cmp(right) == 0
}

func normalizeJSONValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		normalized, err := normalizeJSONNumber(value)
		if err != nil {
			return nil, err
		}
		return json.Number(normalized), nil
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

// normalizeJSONNumber emits one decimal representation for all spellings of
// the same JSON number. PostgreSQL jsonb compares numeric values, not their
// lexical source forms (for example, 1, 1.0, and 1e0 are equal).
func normalizeJSONNumber(number json.Number) (string, error) {
	raw := string(number)
	sign := ""
	if strings.HasPrefix(raw, "-") {
		sign = "-"
		raw = raw[1:]
	}
	exponent := 0
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		parsed, err := strconv.Atoi(raw[index+1:])
		if err != nil {
			return "", fmt.Errorf("invalid numeric exponent")
		}
		exponent = parsed
		raw = raw[:index]
	}
	dot := strings.IndexByte(raw, '.')
	integerDigits := raw
	if dot >= 0 {
		integerDigits = raw[:dot]
	}
	digits := strings.ReplaceAll(raw, ".", "")
	if strings.Trim(digits, "0") == "" {
		return "0", nil
	}
	leading := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = digits[leading:]
	decimalPosition := len(integerDigits) + exponent - leading
	var result string
	switch {
	case decimalPosition <= 0:
		result = "0." + strings.Repeat("0", -decimalPosition) + digits
	case decimalPosition >= len(digits):
		result = digits + strings.Repeat("0", decimalPosition-len(digits))
	default:
		result = digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if dot >= 0 || exponent < 0 {
		if point := strings.IndexByte(result, '.'); point >= 0 {
			fraction := strings.TrimRight(result[point+1:], "0")
			if fraction == "" {
				result = result[:point]
			} else {
				result = result[:point+1] + fraction
			}
		}
	}
	return sign + result, nil
}

// GenerationFieldsAllowed implements the H00 presence rule independently of
// any request or HTTP type: every supplied field must occur in the durable
// allowlist. An empty allowlist therefore denies every supplied override.
func GenerationFieldsAllowed(supplied, allowed []string) (bool, error) {
	canonical, err := CanonicalSet("allowed_generation", allowed)
	if err != nil {
		return false, err
	}
	set := make(map[string]struct{}, len(canonical))
	for _, field := range canonical {
		set[field] = struct{}{}
	}
	for _, field := range supplied {
		if _, ok := set[field]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// ClientLockKey returns the client/v1 transaction advisory key.
func ClientLockKey(clientID int64) (int64, error) {
	if clientID < 0 {
		return 0, fmt.Errorf("client id must not be negative")
	}
	return key(clientPrefix, strconv.FormatInt(clientID, 10)), nil
}

// ClientNameLockKey serializes creation of a client identity before a numeric
// client ID exists. It is a separate namespace from existing client-ID locks.
func ClientNameLockKey(name string) (int64, error) {
	if err := validSlug(name); err != nil {
		return 0, err
	}
	return key(clientNamePrefix, name), nil
}

// PolicyLockKey returns the policy/v1 transaction advisory key.
func PolicyLockKey(slug string) (int64, error) {
	if err := validSlug(slug); err != nil {
		return 0, err
	}
	return key(policyPrefix, slug), nil
}

// GrantAdmissionLockKey returns the grant-admission/v1 transaction advisory
// key. Its identity is the decimal client ID, one NUL byte, and the slug.
func GrantAdmissionLockKey(clientID int64, slug string) (int64, error) {
	if clientID < 0 {
		return 0, fmt.Errorf("client id must not be negative")
	}
	if err := validSlug(slug); err != nil {
		return 0, err
	}
	return key(grantPrefix, strconv.FormatInt(clientID, 10)+"\x00"+slug), nil
}

// GlobalPolicyApplyLockKey returns the global policy-apply/v1 key.
func GlobalPolicyApplyLockKey() int64 { return key(globalPrefix, GlobalApplyID) }

func key(prefix byte, identity string) int64 {
	digest := advisoryKeyDigest(identity)
	low := uint64(0)
	for _, b := range digest[25:32] {
		low = (low << 8) | uint64(b)
	}
	return int64(uint64(prefix)<<56 | low)
}

func validSlug(slug string) error {
	if slug == "" || strings.ContainsRune(slug, '\x00') || !utf8.ValidString(slug) {
		return fmt.Errorf("slug is empty or contains an invalid character")
	}
	for i, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("slug contains invalid character at byte %d", i)
	}
	return nil
}
