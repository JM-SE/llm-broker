package policy

import "testing"

func TestEffectiveOutputBytesPrefersExplicitBound(t *testing.T) {
	if got := EffectiveOutputBytes(1024, 16384, true); got != 16384 {
		t.Fatalf("explicit bound = %d, want 16384", got)
	}
}

func TestEffectiveOutputBytesFallsBackToLegacyHeuristic(t *testing.T) {
	if got := EffectiveOutputBytes(512, 0, false); got != 2048 {
		t.Fatalf("legacy fallback = %d, want 512*4=2048", got)
	}
	// An explicit value is ignored unless the explicit flag is set, so a
	// populated-but-unflagged bound can never silently widen enforcement.
	if got := EffectiveOutputBytes(1024, 16384, false); got != 4096 {
		t.Fatalf("unflagged fallback = %d, want 1024*4=4096", got)
	}
}
