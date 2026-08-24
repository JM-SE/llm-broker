package policy

import (
	"encoding/hex"
	"reflect"
	"testing"
)

func TestCanonicalSetRejectsBeforeSorting(t *testing.T) {
	if _, err := CanonicalSet("allowed_capabilities", []string{"b", "a", "b"}); err == nil {
		t.Fatal("duplicate capability should be rejected")
	}
	got, err := CanonicalSet("allowed_capabilities", []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical set = %#v, want %#v", got, want)
	}
	got, err = CanonicalSet("allowed_capabilities", nil)
	if err != nil || got != nil {
		t.Fatalf("empty set = %#v, %v", got, err)
	}
}

func TestAdvisoryLockGoldenVectors(t *testing.T) {
	tests := []struct {
		name string
		got  func() (int64, error)
		want int64
	}{
		{"client zero", func() (int64, error) { return ClientLockKey(0) }, 1236756217468770281},
		{"client decimal", func() (int64, error) { return ClientLockKey(42) }, 1231695349915746377},
		{"client name", func() (int64, error) { return ClientNameLockKey("client-name-default") }, 1300624029356338018},
		{"policy", func() (int64, error) { return PolicyLockKey("default") }, 2446601638269446287},
		{"policy new", func() (int64, error) { return PolicyLockKey("new-workload") }, 2436428420468291905},
		{"grant", func() (int64, error) { return GrantAdmissionLockKey(42, "default") }, 3578628098429521034},
		{"grant zero", func() (int64, error) { return GrantAdmissionLockKey(0, "new-workload") }, 3561498383274128403},
		{"global", func() (int64, error) { return GlobalPolicyApplyLockKey(), nil }, 4727998623323919485},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || got <= 0 {
				t.Fatalf("key = %d, want positive literal %d", got, tt.want)
			}
		})
	}
}

func TestForcedSameDomainAdvisoryKeyCollision(t *testing.T) {
	original := advisoryKeyDigest
	t.Cleanup(func() { advisoryKeyDigest = original })
	advisoryKeyDigest = func(string) [32]byte {
		var digest [32]byte
		for i := range digest {
			digest[i] = 0xA5
		}
		return digest
	}

	left, err := PolicyLockKey("collision-left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := PolicyLockKey("collision-right")
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("forced policy keys differ: left=%d right=%d", left, right)
	}

	client, err := ClientLockKey(1)
	if err != nil {
		t.Fatal(err)
	}
	if client == left {
		t.Fatalf("forced policy collision crossed advisory domains: policy=%d client=%d", left, client)
	}
}

func TestTopologyDigestGoldenAndSecretIndependence(t *testing.T) {
	topology := Topology{
		Capabilities: map[string]string{"c": "b"},
		Backends: map[string]Backend{"b": {
			Type: "fake", BaseURL: "https://runtime.example/v1", Model: "alias",
			Features: []string{"structured_output"},
		}},
	}
	digest := Digest(topology)
	if got := hex.EncodeToString(digest[:]); got != "97778fd52cffd61665b9759b730edbdce2210d439bd5280a77840817efc4bdb3" {
		t.Fatalf("digest = %s", got)
	}
	changed := topology
	changed.Backends = map[string]Backend{"b": {Type: "fake", BaseURL: topology.Backends["b"].BaseURL, Model: "other", Features: []string{"structured_output"}}}
	if Digest(changed) == digest {
		t.Fatal("routing model change did not change topology digest")
	}
}

func TestTopologyDigestChangesForEveryRoutingFact(t *testing.T) {
	base := Topology{
		Capabilities: map[string]string{"instruct": "primary"},
		Backends: map[string]Backend{"primary": {
			Type: "openai_compatible", BaseURL: "https://runtime.example/v1", Model: "alias",
			Features: []string{"structured_output", "tool_calling"},
		}},
	}
	baseDigest := Digest(base)

	mutations := map[string]func(Topology){
		"capability mapping": func(topology Topology) {
			topology.Capabilities["instruct"] = "secondary"
		},
		"backend type": func(topology Topology) {
			backend := topology.Backends["primary"]
			backend.Type = "fake"
			topology.Backends["primary"] = backend
		},
		"backend URL": func(topology Topology) {
			backend := topology.Backends["primary"]
			backend.BaseURL = "https://other-runtime.example/v1"
			topology.Backends["primary"] = backend
		},
		"backend model": func(topology Topology) {
			backend := topology.Backends["primary"]
			backend.Model = "other-alias"
			topology.Backends["primary"] = backend
		},
		"backend features": func(topology Topology) {
			backend := topology.Backends["primary"]
			backend.Features = append(backend.Features, "vision")
			topology.Backends["primary"] = backend
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := Topology{
				Capabilities: map[string]string{},
				Backends:     map[string]Backend{},
			}
			for name, backend := range base.Backends {
				candidate.Backends[name] = Backend{
					Type: backend.Type, BaseURL: backend.BaseURL, Model: backend.Model,
					Features: append([]string(nil), backend.Features...),
				}
			}
			for name, backend := range base.Capabilities {
				candidate.Capabilities[name] = backend
			}
			mutate(candidate)
			if got := Digest(candidate); got == baseDigest {
				t.Fatalf("routing mutation did not change digest: %x", got)
			}
		})
	}

	reordered := Topology{
		Capabilities: map[string]string{"instruct": "primary"},
		Backends: map[string]Backend{"primary": {
			Type: "openai_compatible", BaseURL: "https://runtime.example/v1", Model: "alias",
			Features: []string{"tool_calling", "structured_output"},
		}},
	}
	if got := Digest(reordered); got != baseDigest {
		t.Fatalf("feature ordering changed digest: got %x want %x", got, baseDigest)
	}
}

func TestCanonicalJSONSemanticEquality(t *testing.T) {
	left, err := CanonicalJSON([]byte(`{"max_tokens":{"max":2048},"temperature":{"min":0.2}}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalJSON([]byte("{ \"temperature\": { \"min\": 0.2 }, \"max_tokens\": { \"max\": 2048 } }"))
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) || !JSONSemanticallyEqual(left, right) {
		t.Fatalf("semantic JSON mismatch: %q vs %q", left, right)
	}
	if _, err := CanonicalJSON([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("trailing JSON should be rejected")
	}
	if !JSONSemanticallyEqual([]byte(`{"temperature":1,"nested":[2.50,-0.0]}`),
		[]byte(`{"nested":[2.5,0],"temperature":1.0}`)) {
		t.Fatal("numeric JSON forms should compare like PostgreSQL jsonb")
	}
	if JSONSemanticallyEqual([]byte(`{"value":1}`), []byte(`{"value":1.01}`)) {
		t.Fatal("different numeric JSON values compared equal")
	}
	canonical, err := CanonicalJSON([]byte(`{"a":1.0,"b":1e0,"c":0.00100}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"a":1,"b":1,"c":0.001}`; got != want {
		t.Fatalf("numeric canonical JSON = %s, want %s", got, want)
	}
}
