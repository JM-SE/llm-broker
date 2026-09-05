package compat

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/config"
	"github.com/JM-SE/llm-broker/internal/domain"
)

func validManifestJSON() string {
	return `{
  "version":"consumer-compat/v1",
  "api_range": ">=0.1.1 <0.2.0",
  "workload":"generic-workload",
  "capability":"generic-capability",
  "generation":{"temperature":0.7,"max_tokens":128,"stop":["done","stop"]},
  "response":{"kind":"text"},
  "max_request_content_bytes":131072,
  "required_min_output_bytes":1024,
  "min_workload_timeout_seconds":60,
  "min_backend_response_start_timeout_seconds":0,
  "max_acceptable_attempts":1,
  "consumer_overall_deadline_seconds":120
}`
}

func validConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Decode(strings.NewReader(`{
  "version":2,
  "api":{"listen":"127.0.0.1:8010"},
  "database":{"url_env":"DATABASE_URL","provisioner_url_env":"PROVISIONER_URL","migration_url_env":"MIGRATION_URL"},
  "retention":{"payload":"1h","result":"2h","metadata":"3h"},
  "backends":{"backend":{"type":"fake"}},
  "capabilities":{"generic-capability":{"backend":"backend"}},
  "workloads":{"generic-workload":{"priority_class":"normal","max_attempts":1,"max_output_bytes":2048,"allowed_capabilities":["generic-capability"],"allowed_generation":{"max_tokens":{"max":128},"temperature":{"min":0,"max":1},"stop":{"max_items":2}},"workload_timeout_seconds":60}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParseStrictAndSemanticDigest(t *testing.T) {
	left, err := Parse(strings.NewReader(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse(strings.NewReader(`{
 "consumer_overall_deadline_seconds":120,"max_acceptable_attempts":1,
 "min_backend_response_start_timeout_seconds":0,"min_workload_timeout_seconds":60,
 "required_min_output_bytes":1024,"max_request_content_bytes":131072,
 "response":{"kind":"text"},"generation":{"stop":["stop","done","done"],"max_tokens":128,"temperature":0.7},
 "capability":"generic-capability","workload":"generic-workload","api_range":">=0.1.1    <0.2.0","version":"consumer-compat/v1"
}`))
	if err != nil {
		t.Fatal(err)
	}
	leftDigest, err := left.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := right.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("format/order normalization changed digest: %x != %x", leftDigest, rightDigest)
	}
	if got := left.Generation.NormalizedStop(); len(got) != 2 || got[0] != "done" || got[1] != "stop" {
		t.Fatalf("normalized stop = %#v", got)
	}
}

func TestParseRejectsFrozenInvalidClasses(t *testing.T) {
	cases := []string{
		strings.Replace(validManifestJSON(), `"version":"consumer-compat/v1"`, `"version":"consumer-compat/v2"`, 1),
		strings.Replace(validManifestJSON(), `"response":{"kind":"text"}`, `"response":{"kind":"text","extra":true}`, 1),
		strings.Replace(validManifestJSON(), `"workload":"generic-workload"`, `"workload":null`, 1),
		strings.Replace(validManifestJSON(), `"capability":"generic-capability"`, `"capability":"*"`, 1),
		strings.Replace(validManifestJSON(), `"response":{"kind":"text"}`, `"response":{"kind":"text"},"secret":"sentinel"`, 1),
		strings.Replace(validManifestJSON(), `"max_request_content_bytes":131072`, `"max_request_content_bytes":0`, 1),
		strings.Replace(validManifestJSON(), `"generation":{"temperature":0.7,"max_tokens":128,"stop":["done","stop"]}`, `"generation":{"temperature":null}`, 1),
		strings.Replace(validManifestJSON(), `"version":"consumer-compat/v1"`, `"version":"consumer-compat/v1","version":"consumer-compat/v1"`, 1),
	}
	for i, raw := range cases {
		if _, err := Parse(strings.NewReader(raw)); err == nil {
			t.Errorf("case %d was accepted", i)
		}
	}
}

func TestParseRejectsOversizedAndDeepDocuments(t *testing.T) {
	if _, err := Parse(strings.NewReader(strings.Repeat("x", MaxManifestBytes+1))); err == nil {
		t.Fatal("oversized document was accepted")
	}
	deep := `{"version":"consumer-compat/v1","api_range":">=0.1.1","workload":"generic-workload","capability":"generic-capability","generation":{"max_tokens":128,"temperature":0.7,"stop":[]},"response":{"kind":"text"},"max_request_content_bytes":1,"required_min_output_bytes":1,"min_workload_timeout_seconds":0,"min_backend_response_start_timeout_seconds":0,"max_acceptable_attempts":1,"consumer_overall_deadline_seconds":2,"nested":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":true}}}}}}}}}`
	if _, err := Parse(strings.NewReader(deep)); err == nil {
		t.Fatal("deep document was accepted")
	}
}

// TestParseRejectsBoundedLimitClasses pins the N-1/N/N+1 contract for every
// named parser bound: too many properties, too many items, and oversized
// strings or property names fail, while the N-1 value passes the raw bound
// walker exactly at the limit.
func TestParseRejectsBoundedLimitClasses(t *testing.T) {
	// Property count: the walker allows MaxManifestProperties per object.
	// A sibling object with exactly MaxManifestProperties passes the count
	// check, while one more fails.
	padProps := func(n int, seed string) string {
		var b strings.Builder
		b.WriteString(`{"version":"consumer-compat/v1","padding":{`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`"k` + seed + strconv.Itoa(i) + `":true`)
		}
		b.WriteString(`}}`)
		return b.String()
	}
	walkRejects := func(name, raw string) {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(raw))
		if err := walkValue(dec, 0); err == nil {
			t.Fatalf("%s: walker accepted a bound violation", name)
		}
		if _, err := Parse(strings.NewReader(raw)); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	items := func(n int) string {
		var b strings.Builder
		b.WriteString(`{"version":"consumer-compat/v1","padding":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Itoa(i))
		}
		b.WriteString(`]}`)
		return b.String()
	}
	exact := strings.Repeat("s", MaxManifestString)

	// F1 pins every frozen vector-file invalid class through the same public
	// entry point the bundle runner uses: property/item/string bounds,
	// duplicate keys, deep nesting, and unknown fields all fail closed.
	walkRejects("too-many-properties", padProps(MaxManifestProperties+1, "c"))
	walkRejects("too-many-items", items(MaxManifestItems+1))
	walkRejects("string-too-long", `{"version":"consumer-compat/v1","padding":"`+exact+`s"}`)
	walkRejects("property-name-too-long", `{"version":"consumer-compat/v1","`+exact+`s":true}`)

	// The walker must pass the count at the limit and fail past it. The
	// sibling "padding" object uses unknown keys, so a walker-level pass
	// still fails closed semantically on unknown fields; the direct walker
	// calls below pin the count boundary precisely.
	atLimit := padProps(MaxManifestProperties, "a")
	decAt := json.NewDecoder(strings.NewReader(atLimit))
	if err := walkValue(decAt, 0); err != nil {
		t.Fatalf("walker rejected exactly %d properties: %v", MaxManifestProperties, err)
	}
	overLimit := padProps(MaxManifestProperties+1, "b")
	decOver := json.NewDecoder(strings.NewReader(overLimit))
	if err := walkValue(decOver, 0); err == nil {
		t.Fatalf("%d-property object passed the walker", MaxManifestProperties+1)
	}
	if _, err := Parse(strings.NewReader(overLimit)); err == nil {
		t.Fatal("33-property object was accepted")
	}

	// Array items: exactly MaxManifestItems pass the walker, one more fails.
	decItemsAt := json.NewDecoder(strings.NewReader(items(MaxManifestItems)))
	if err := walkValue(decItemsAt, 0); err != nil {
		t.Fatalf("walker rejected exactly %d items: %v", MaxManifestItems, err)
	}
	decItemsOver := json.NewDecoder(strings.NewReader(items(MaxManifestItems + 1)))
	if err := walkValue(decItemsOver, 0); err == nil {
		t.Fatalf("%d-item array passed the walker", MaxManifestItems+1)
	}
	if _, err := Parse(strings.NewReader(items(MaxManifestItems + 1))); err == nil {
		t.Fatal("17-item array was accepted")
	}

	// String values and property names: exactly MaxManifestString passes the
	// walker, one byte more fails.
	svalAt := `{"version":"consumer-compat/v1","padding":"` + exact + `"}`
	decStrAt := json.NewDecoder(strings.NewReader(svalAt))
	if err := walkValue(decStrAt, 0); err != nil {
		t.Fatalf("walker rejected an exactly %d-byte string: %v", MaxManifestString, err)
	}
	long := exact + "s"
	sval := `{"version":"consumer-compat/v1","padding":"` + long + `"}`
	if _, err := Parse(strings.NewReader(sval)); err == nil {
		t.Fatal("257-byte string was accepted")
	}
	pname := `{"version":"consumer-compat/v1","` + long + `":true}`
	if _, err := Parse(strings.NewReader(pname)); err == nil {
		t.Fatal("257-byte property name was accepted")
	}
}

// TestDesiredChecksConfigOnlyAndStableFindings checks that the desired
// evaluation reads only validated configuration plus the manifest.
func TestDesiredChecksConfigOnlyAndStableFindings(t *testing.T) {
	manifest, err := Parse(strings.NewReader(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := EvaluateDesired(validConfig(t), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Compatible() {
		t.Fatalf("valid generic profile was rejected: %#v", decision.Findings)
	}
	// A fake adapter has no declared response-start equivalent only when the
	// consumer asks for a positive budget. This profile deliberately asks for
	// none.
	if decision.Findings[FindingConsumerDeadlineTooShort] != 0 {
		t.Fatal("valid deadline was rejected")
	}
	if decision.Findings[FindingExplicitOutputBytesRequired] != 0 {
		t.Fatal("explicit output bytes should be accepted")
	}

	changed := manifest
	changed.MaxRequestContentBytes = domain.MaxCombinedMessageBytes + 1
	changed.RequiredMinOutputBytes = 4096
	changed.MinBackendResponseStartTimeoutSeconds = 1
	changed.ConsumerOverallDeadlineSeconds = 60
	decision, err = EvaluateDesired(validConfig(t), changed)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []FindingCode{FindingRequestContentLimitInsufficient, FindingOutputBytesLimitInsufficient, FindingBackendResponseStartTimeoutInsuff, FindingConsumerDeadlineTooShort} {
		if decision.Findings[code] != 1 {
			t.Errorf("missing finding %s in %#v", code, decision.Findings)
		}
	}
}

func TestEffectiveAddsOnlyReadOnlyStateFindings(t *testing.T) {
	manifest, err := Parse(strings.NewReader(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig(t)
	workloads, err := cfg.PolicyProjections()
	if err != nil {
		t.Fatal(err)
	}
	state := EffectiveState{ClientFound: true, ClientEnabled: false, PolicyFound: true, Policy: workloads[0]}
	decision, err := EvaluateEffective(cfg, manifest, state)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Findings[FindingClientDisabled] != 1 || decision.Findings[FindingWorkloadGrantMissing] != 1 {
		t.Fatalf("effective state findings = %#v", decision.Findings)
	}
	if decision.Findings[FindingDurablePolicyDrift] != 0 {
		t.Fatal("equal durable policy was reported as drift")
	}

	state.GrantPresent = true
	state.Policy.TopologyDigest[0]++
	decision, err = EvaluateEffective(cfg, manifest, state)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Findings[FindingTopologyMismatch] != 1 {
		t.Fatalf("topology mismatch missing: %#v", decision.Findings)
	}
}

func TestManifestCanonicalUsesOnlyContractFields(t *testing.T) {
	manifest, err := Parse(strings.NewReader(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(canonical, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 12 || value["secret"] != nil {
		t.Fatalf("canonical field set = %#v", value)
	}
	if bytes.Contains(canonical, []byte("database")) || bytes.Contains(canonical, []byte("provider")) {
		t.Fatalf("canonical manifest contains forbidden topology data: %s", canonical)
	}
}

func TestEffectivePolicyUsesIndependentOutputBytes(t *testing.T) {
	cfg := validConfig(t)
	manifest, err := Parse(strings.NewReader(validManifestJSON()))
	if err != nil {
		t.Fatal(err)
	}
	workloads, err := cfg.PolicyProjections()
	if err != nil {
		t.Fatal(err)
	}
	actual := workloads[0]
	actual.MaxOutputBytes = 0
	actual.MaxOutputBytesExplicit = false
	decision, err := EvaluateEffective(cfg, manifest, EffectiveState{
		ClientFound: true, ClientEnabled: true, GrantPresent: true, PolicyFound: true, Policy: actual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Findings[FindingExplicitOutputBytesRequired] != 1 {
		t.Fatal("durable NULL output bound passed compatibility")
	}
}

func TestDesiredResponseStartBudgetUsesAdapterDefaultWithoutCalls(t *testing.T) {
	cfg := validConfig(t)
	manifest, err := Parse(strings.NewReader(strings.Replace(validManifestJSON(),
		`"min_backend_response_start_timeout_seconds":0`,
		`"min_backend_response_start_timeout_seconds":15`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := EvaluateDesired(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Findings[FindingBackendResponseStartTimeoutInsufficient] != 1 {
		t.Fatal("fake adapter without a declared equivalent did not fail closed")
	}

	openAI := validConfig(t)
	openAI.Backends["backend"] = config.BackendConfig{
		Type: config.AdapterOpenAICompatible, BaseURL: "http://runtime.invalid/v1",
		Model: "alias", CredentialEnv: "BACKEND_KEY",
	}
	decision, err = EvaluateDesired(openAI, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Findings[FindingBackendResponseStartTimeoutInsufficient] != 0 {
		t.Fatal("default OpenAI-compatible response header timeout was not applied")
	}
}

// TestEffectiveTopologyAndDriftAreIndependent freezes the F10 contract: a
// topology-only change yields exactly topology_mismatch, and a mutable-bound
// change yields exactly durable_policy_drift. Neither masks the other.
func TestEffectiveTopologyAndDriftAreIndependent(t *testing.T) {
	newState := func(t *testing.T) (Manifest, *config.Config, EffectiveState) {
		t.Helper()
		manifest, err := Parse(strings.NewReader(validManifestJSON()))
		if err != nil {
			t.Fatal(err)
		}
		cfg := validConfig(t)
		workloads, err := cfg.PolicyProjections()
		if err != nil {
			t.Fatal(err)
		}
		return manifest, cfg, EffectiveState{
			ClientFound: true, ClientEnabled: true, GrantPresent: true,
			PolicyFound: true, Policy: workloads[0],
		}
	}
	t.Run("topology-only", func(t *testing.T) {
		manifest, cfg, state := newState(t)
		state.Policy.TopologyDigest[0]++
		decision, err := EvaluateEffective(cfg, manifest, state)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Findings[FindingTopologyMismatch] != 1 {
			t.Fatalf("topology-only findings = %#v", decision.Findings)
		}
		if decision.Findings[FindingDurablePolicyDrift] != 0 {
			t.Fatalf("topology-only change reported drift: %#v", decision.Findings)
		}
	})
	t.Run("mutable-bound-only", func(t *testing.T) {
		manifest, cfg, state := newState(t)
		state.Policy.MaxOutputBytes = 4096
		decision, err := EvaluateEffective(cfg, manifest, state)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Findings[FindingDurablePolicyDrift] != 1 {
			t.Fatalf("mutable-only findings = %#v", decision.Findings)
		}
		if decision.Findings[FindingTopologyMismatch] != 0 {
			t.Fatalf("mutable-only change reported topology mismatch: %#v", decision.Findings)
		}
	})
}
