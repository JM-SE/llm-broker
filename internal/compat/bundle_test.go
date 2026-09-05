package compat_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type bundleMeta struct {
	BundleVersion string   `json:"bundle_version"`
	Digest        string   `json:"digest"`
	Vectors       []string `json:"vectors"`
}

// TestCanonicalBundleDigest verifies the consumer-compat/v1 fixture bundle
// digest using the frozen algorithm: the SHA-256 over the concatenation of
// the lowercase hex SHA-256 of each vector file's bytes, in bundle order.
func TestCanonicalBundleDigest(t *testing.T) {
	dir := filepath.Join("..", "..", "contracts", "consumer-compat", "v1")
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatalf("read bundle.json: %v", err)
	}
	var meta bundleMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode bundle.json: %v", err)
	}
	if meta.BundleVersion != "consumer-compat/v1" {
		t.Fatalf("bundle_version = %q", meta.BundleVersion)
	}
	if len(meta.Vectors) != 5 {
		t.Fatalf("vector count = %d", len(meta.Vectors))
	}
	seen := map[string]bool{}
	for _, name := range meta.Vectors {
		if filepath.Base(name) != name || seen[name] {
			t.Fatalf("vector name %q is not a unique base name", name)
		}
		seen[name] = true
	}
	want := map[string]bool{
		"valid-manifests.json": true, "invalid-manifests.json": true,
		"decisions.json": true, "terminal-envelopes.json": true,
		"transport-outcomes.json": true,
	}
	if len(seen) != len(want) {
		for name := range want {
			if !seen[name] {
				t.Fatalf("vector %q missing", name)
			}
		}
		t.Fatalf("unexpected vectors: %v", meta.Vectors)
	}
	for _, name := range meta.Vectors {
		if !want[name] {
			t.Fatalf("unexpected vector %q", name)
		}
	}
	hasher := sha256.New()
	for _, name := range meta.Vectors {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read vector %s: %v", name, err)
		}
		sum := sha256.Sum256(content)
		if _, err := hasher.Write([]byte(hex.EncodeToString(sum[:]))); err != nil {
			t.Fatalf("hash write: %v", err)
		}
		if err := json.Unmarshal(content, new(any)); err != nil {
			t.Fatalf("vector %s is not valid JSON: %v", name, err)
		}
	}
	computed := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if meta.Digest != computed {
		t.Fatalf("bundle digest = %s, computed %s", meta.Digest, computed)
	}
}
