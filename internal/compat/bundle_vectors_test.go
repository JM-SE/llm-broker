package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/config"
)

const bundleDir = "../../contracts/consumer-compat/v1"

// TestBundleVectorsExecute makes the canonical consumer-compat/v1 fixture
// bundle executable: every listed vector is loaded from disk and run through
// the production parser and evaluator, so the bundle is a contract rather
// than decoration.
func TestBundleVectorsExecute(t *testing.T) {
	metaRaw, err := os.ReadFile(filepath.Join(bundleDir, "bundle.json"))
	if err != nil {
		t.Fatalf("read bundle.json: %v", err)
	}
	var meta struct {
		BundleVersion string   `json:"bundle_version"`
		Digest        string   `json:"digest"`
		Vectors       []string `json:"vectors"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("decode bundle.json: %v", err)
	}
	if meta.BundleVersion != Version {
		t.Fatalf("bundle_version = %q, want %q", meta.BundleVersion, Version)
	}
	hasher := sha256.New()
	for _, name := range meta.Vectors {
		content, err := os.ReadFile(filepath.Join(bundleDir, name))
		if err != nil {
			t.Fatalf("read vector %s: %v", name, err)
		}
		sum := sha256.Sum256(content)
		if _, err := hasher.Write([]byte(hex.EncodeToString(sum[:]))); err != nil {
			t.Fatalf("hash write: %v", err)
		}
	}
	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != meta.Digest {
		t.Fatalf("bundle digest = %s, computed %s", meta.Digest, got)
	}

	t.Run("valid-manifests", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Join(bundleDir, "valid-manifests.json"))
		if err != nil {
			t.Fatal(err)
		}
		var docs []json.RawMessage
		if err := json.Unmarshal(content, &docs); err != nil {
			t.Fatal(err)
		}
		if len(docs) != 2 {
			t.Fatalf("valid manifest count = %d, want 2 (two generic onboardings)", len(docs))
		}
		workloads := map[string]bool{}
		for i, doc := range docs {
			m, err := Parse(strings.NewReader(string(doc)))
			if err != nil {
				t.Fatalf("valid manifest %d rejected: %v", i, err)
			}
			if _, err := m.Digest(); err != nil {
				t.Fatalf("valid manifest %d digest failed: %v", i, err)
			}
			if workloads[m.Workload] {
				t.Fatalf("duplicate valid workload %q", m.Workload)
			}
			workloads[m.Workload] = true
		}
	})

	t.Run("invalid-manifests", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Join(bundleDir, "invalid-manifests.json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []struct {
			Name     string         `json:"name"`
			Manifest map[string]any `json:"manifest"`
			Raw      string         `json:"raw"`
		}
		if err := json.Unmarshal(content, &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != 6 {
			t.Fatalf("invalid manifest count = %d, want 6", len(entries))
		}
		for _, entry := range entries {
			var raw string
			if entry.Raw != "" {
				raw = entry.Raw
			} else {
				encoded, err := json.Marshal(entry.Manifest)
				if err != nil {
					t.Fatalf("%s: marshal: %v", entry.Name, err)
				}
				raw = string(encoded)
			}
			if _, err := Parse(strings.NewReader(raw)); err == nil {
				t.Errorf("invalid manifest %q was accepted", entry.Name)
			}
		}
	})

	t.Run("decisions", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Join(bundleDir, "decisions.json"))
		if err != nil {
			t.Fatal(err)
		}
		var vectors []struct {
			Name      string          `json:"name"`
			Mode      string          `json:"mode"`
			Config    json.RawMessage `json:"config"`
			Manifest  json.RawMessage `json:"manifest"`
			Effective *struct {
				ClientFound      bool                       `json:"client_found"`
				ClientEnabled    bool                       `json:"client_enabled"`
				GrantPresent     bool                       `json:"grant_present"`
				PolicyFound      bool                       `json:"policy_found"`
				Topology         string                     `json:"topology"`
				DurableOverrides map[string]json.RawMessage `json:"durable_overrides"`
			} `json:"effective"`
			Status   string `json:"status"`
			Findings []struct {
				Code  string `json:"code"`
				Count int    `json:"count"`
			} `json:"findings"`
		}
		if err := json.Unmarshal(content, &vectors); err != nil {
			t.Fatal(err)
		}
		if len(vectors) != 23 {
			t.Fatalf("decision count = %d, want 23", len(vectors))
		}
		for _, vector := range vectors {
			t.Run(vector.Name, func(t *testing.T) {
				cfg, err := config.Decode(strings.NewReader(string(vector.Config)))
				if err != nil {
					t.Fatalf("decode config: %v", err)
				}
				manifest, err := Parse(strings.NewReader(string(vector.Manifest)))
				if err != nil {
					t.Fatalf("parse manifest: %v", err)
				}
				var decision Decision
				switch vector.Mode {
				case "desired":
					decision, err = EvaluateDesired(cfg, manifest)
				case "effective":
					if vector.Effective == nil {
						t.Fatal("effective vector lacks effective state")
					}
					workloads, err := cfg.PolicyProjections()
					if err != nil {
						t.Fatalf("projections: %v", err)
					}
					var found bool
					var idx int
					for i := range workloads {
						if workloads[i].Name == manifest.Workload {
							idx = i
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("workload %q missing from projections", manifest.Workload)
					}
					statePolicy := workloads[idx]
					for key, raw := range vector.Effective.DurableOverrides {
						switch key {
						case "max_output_bytes":
							var value int
							if err := json.Unmarshal(raw, &value); err != nil {
								t.Fatalf("override %s: %v", key, err)
							}
							statePolicy.MaxOutputBytes = value
							statePolicy.MaxOutputBytesExplicit = true
						default:
							t.Fatalf("unsupported durable override %q", key)
						}
					}
					if vector.Effective.Topology == "mismatch" {
						statePolicy.TopologyDigest[0]++
					} else if vector.Effective.Topology != "match" && vector.Effective.Topology != "" {
						t.Fatalf("unknown topology %q", vector.Effective.Topology)
					}
					decision, err = EvaluateEffective(cfg, manifest, EffectiveState{
						ClientFound:   vector.Effective.ClientFound,
						ClientEnabled: vector.Effective.ClientEnabled,
						GrantPresent:  vector.Effective.GrantPresent,
						PolicyFound:   vector.Effective.PolicyFound,
						Policy:        statePolicy,
					})
				default:
					t.Fatalf("unknown mode %q", vector.Mode)
				}
				if err != nil {
					t.Fatalf("evaluate: %v", err)
				}
				if want := vector.Status == "compatible"; want != decision.Compatible() {
					t.Fatalf("compatible = %v, want %v (findings %#v)", decision.Compatible(), want, decision.Findings)
				}
				want := map[FindingCode]int{}
				for _, f := range vector.Findings {
					want[FindingCode(f.Code)] = f.Count
				}
				if len(decision.Findings) != len(want) {
					t.Fatalf("findings = %#v, want %#v", decision.Findings, want)
				}
				for code, count := range want {
					if decision.Findings[code] != count {
						t.Fatalf("finding %s = %d, want %d (all %#v)", code, decision.Findings[code], count, decision.Findings)
					}
				}
			})
		}
	})

	t.Run("transport-outcomes", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Join(bundleDir, "transport-outcomes.json"))
		if err != nil {
			t.Fatal(err)
		}
		var outcomes []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(content, &outcomes); err != nil {
			t.Fatal(err)
		}
		valid := map[string]bool{"pending": true, "succeeded": true, "failed": true, "cancelled": true, "expired": true}
		seen := map[string]bool{}
		for _, outcome := range outcomes {
			if outcome.Name == "" || seen[outcome.Name] {
				t.Fatalf("bad or duplicate outcome name %q", outcome.Name)
			}
			seen[outcome.Name] = true
			if !valid[outcome.Status] {
				t.Fatalf("outcome %q has invalid status %q", outcome.Name, outcome.Status)
			}
		}
		if len(outcomes) != 5 {
			t.Fatalf("outcome count = %d, want 5", len(outcomes))
		}
		for status := range valid {
			found := false
			for _, outcome := range outcomes {
				if outcome.Status == status {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("terminal status %q has no transport outcome", status)
			}
		}
	})
}

// TestY01ConsumerDigestPin freezes the cross-language contract with the YTPipe
// Y01 profile: the vendored copy of the consumer-owned manifest must parse and
// produce the exact semantic digest the consumer computes. Any
// canonicalization drift on either side fails closed here before it can
// become a false compatibility PASS in the field.
func TestY01ConsumerDigestPin(t *testing.T) {
	raw, err := os.ReadFile("testdata/y01.consumer-compat.json")
	if err != nil {
		t.Fatalf("read y01 manifest: %v", err)
	}
	manifest, err := Parse(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse y01 manifest: %v", err)
	}
	digest, err := manifest.DigestString()
	if err != nil {
		t.Fatalf("digest y01 manifest: %v", err)
	}
	const want = "sha256:60450bde099909b93a79deb03d07576b47b8282c0e0a4ded91aa56dd1a61615e"
	if digest != want {
		t.Fatalf("y01 digest = %s, want %s", digest, want)
	}
}
