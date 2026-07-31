package domain

import (
	"strings"
	"testing"
)

func TestErrorClassTaxonomy(t *testing.T) {
	classes := []ErrorClass{
		ErrorClientInvalid, ErrorPolicyRejected, ErrorBackendRejected,
		ErrorOutputInvalid, ErrorTransientUnsent, ErrorTransientSafe,
		ErrorIndeterminate, ErrorInternal,
	}
	for _, c := range classes {
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	if (ErrorClass("bogus")).Valid() {
		t.Error("bogus class should be invalid")
	}
}

func TestErrorClassRetryable(t *testing.T) {
	// Only transient_unsent and transient_safe auto-retry.
	for _, c := range []ErrorClass{
		ErrorClientInvalid, ErrorPolicyRejected, ErrorBackendRejected,
		ErrorOutputInvalid, ErrorIndeterminate, ErrorInternal,
	} {
		if c.Retryable() {
			t.Errorf("%s must not be retryable", c)
		}
	}
	for _, c := range []ErrorClass{ErrorTransientUnsent, ErrorTransientSafe} {
		if !c.Retryable() {
			t.Errorf("%s must be retryable", c)
		}
	}
}

func TestAgingBoost(t *testing.T) {
	cases := []struct {
		queuedSeconds int64
		want          int
	}{
		{0, 0},
		{1, 0},
		{599, 0},
		{600, 100},   // 10 min
		{1199, 100},  // just under 20 min
		{1200, 200},  // 20 min
		{1800, 300},  // 30 min
		{99999, 300}, // capped
	}
	for _, c := range cases {
		if got := AgingBoost(c.queuedSeconds); got != c.want {
			t.Errorf("AgingBoost(%d) = %d, want %d", c.queuedSeconds, got, c.want)
		}
	}
}

func TestEffectivePriority(t *testing.T) {
	// Aged batch reaches normal; aged normal must never reach high base.
	if got, want := EffectivePriority(PriorityBatch, 1800), 400; got != want {
		t.Errorf("aged batch priority = %d, want %d", got, want)
	}
	if got, want := EffectivePriority(PriorityNormal, 1800), 300; got != want {
		t.Errorf("aged normal priority = %d, want %d", got, want)
	}
	if got := EffectivePriority(PriorityNormal, 99999); got > PriorityHigh.Base() {
		t.Errorf("aged normal must not exceed high base: %d", got)
	}
	if got := EffectivePriority(PriorityHigh, 99999); got != PriorityHigh.Base() {
		t.Errorf("high must not age: got %d, want %d", got, PriorityHigh.Base())
	}
}

func TestParsePriorityClass(t *testing.T) {
	for _, s := range []string{"high", "normal", "batch"} {
		if _, err := ParsePriorityClass(s); err != nil {
			t.Errorf("ParsePriorityClass(%q): %v", s, err)
		}
	}
	if _, err := ParsePriorityClass("urgent"); err == nil {
		t.Error("ParsePriorityClass(urgent) should fail")
	}
	if got := strings.TrimSpace((PriorityHigh).String()); got != "high" {
		t.Errorf("String() = %q", got)
	}
}
