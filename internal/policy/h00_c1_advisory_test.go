package policy

import (
	"crypto/sha256"
	"testing"
)

func TestH00C1AdvisoryLiteralVectorsAreIndependentOfProductionKey(t *testing.T) {
	tests := []struct {
		name     string
		prefix   byte
		identity string
		want     int64
		got      func() (int64, error)
	}{
		{
			name: "client zero", prefix: 0x11, identity: "0",
			want: 1236756217468770281,
			got:  func() (int64, error) { return ClientLockKey(0) },
		},
		{
			name: "client decimal", prefix: 0x11, identity: "42",
			want: 1231695349915746377,
			got:  func() (int64, error) { return ClientLockKey(42) },
		},
		{
			name: "pre-id client name", prefix: 0x12, identity: "client-name-default",
			want: 1300624029356338018,
			got:  func() (int64, error) { return ClientNameLockKey("client-name-default") },
		},
		{
			name: "policy default", prefix: 0x21, identity: "default",
			want: 2446601638269446287,
			got:  func() (int64, error) { return PolicyLockKey("default") },
		},
		{
			name: "policy new workload", prefix: 0x21, identity: "new-workload",
			want: 2436428420468291905,
			got:  func() (int64, error) { return PolicyLockKey("new-workload") },
		},
		{
			name: "grant default", prefix: 0x31, identity: "42\x00default",
			want: 3578628098429521034,
			got:  func() (int64, error) { return GrantAdmissionLockKey(42, "default") },
		},
		{
			name: "grant new workload", prefix: 0x31, identity: "0\x00new-workload",
			want: 3561498383274128403,
			got:  func() (int64, error) { return GrantAdmissionLockKey(0, "new-workload") },
		},
		{
			name: "global apply", prefix: 0x41, identity: GlobalApplyID,
			want: 4727998623323919485,
			got:  func() (int64, error) { return GlobalPolicyApplyLockKey(), nil },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			independent := h00C1AssembleAdvisoryLiteral(tt.prefix, tt.identity)
			if independent != tt.want || independent <= 0 {
				t.Fatalf("independent literal = %d, want positive %d", independent, tt.want)
			}
			got, err := tt.got()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || got <= 0 {
				t.Fatalf("production key = %d, want positive literal %d", got, tt.want)
			}
			if byte(uint64(got)>>56) != tt.prefix {
				t.Fatalf("production namespace = 0x%02x, want 0x%02x", byte(uint64(got)>>56), tt.prefix)
			}
		})
	}
}

func h00C1AssembleAdvisoryLiteral(prefix byte, identity string) int64 {
	digest := sha256.Sum256([]byte(identity))
	var low uint64
	for _, b := range digest[25:] {
		low = (low << 8) | uint64(b)
	}
	return int64(uint64(prefix)<<56 | low)
}

func TestH00C1AdvisoryNamespacesSeparateIdenticalDigestSuffixes(t *testing.T) {
	identity := "42"
	client := h00C1AssembleAdvisoryLiteral(0x11, identity)
	preID := h00C1AssembleAdvisoryLiteral(0x12, identity)
	policyKey := h00C1AssembleAdvisoryLiteral(0x21, identity)
	grant := h00C1AssembleAdvisoryLiteral(0x31, identity)
	global := h00C1AssembleAdvisoryLiteral(0x41, identity)
	keys := []int64{client, preID, policyKey, grant, global}
	seen := make(map[int64]struct{}, len(keys))
	for _, key := range keys {
		if key <= 0 {
			t.Fatalf("namespace key = %d, want positive", key)
		}
		if _, ok := seen[key]; ok {
			t.Fatalf("advisory namespace collision for key %d", key)
		}
		seen[key] = struct{}{}
	}
	if byte(uint64(client)>>56) != 0x11 || byte(uint64(preID)>>56) != 0x12 ||
		byte(uint64(policyKey)>>56) != 0x21 || byte(uint64(grant)>>56) != 0x31 ||
		byte(uint64(global)>>56) != 0x41 {
		t.Fatalf("namespace prefixes are not assembled exactly: %v", keys)
	}
}

func TestH00C1ForcedSameDomainCollisionUsesOnlyLow56Bits(t *testing.T) {
	original := advisoryKeyDigest
	t.Cleanup(func() { advisoryKeyDigest = original })
	advisoryKeyDigest = func(string) [32]byte {
		var digest [32]byte
		for i := range digest {
			digest[i] = 0xa5
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
	// The production seam above returns a fixed digest, not SHA-256 of an
	// identity. Compute the expected positive assembly independently instead.
	var fixed [32]byte
	for i := range fixed {
		fixed[i] = 0xa5
	}
	var low uint64
	for _, b := range fixed[25:] {
		low = (low << 8) | uint64(b)
	}
	want := int64(uint64(0x21)<<56 | low)
	if left != right || left != want || left <= 0 {
		t.Fatalf("forced same-domain keys = %d/%d, want positive literal %d", left, right, want)
	}
	client, err := ClientLockKey(1)
	if err != nil {
		t.Fatal(err)
	}
	if client == left || byte(uint64(client)>>56) != 0x11 {
		t.Fatalf("forced policy collision crossed client namespace: policy=%d client=%d", left, client)
	}
}
