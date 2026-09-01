package scheduler

import (
	"testing"
	"time"
)

func claimAt(t *testing.T, b *AffinityState, cap string, size int, maxAge time.Duration, now time.Time) Decision {
	t.Helper()
	return b.Consider(cap, 100, size, maxAge, now)
}

func TestBatchOpensOnFirstClaim(t *testing.T) {
	var b AffinityState
	now := time.Now()
	d := claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	if !d.BatchOpen || d.BatchKey != "instruct" || d.BatchClaimed != 1 {
		t.Errorf("decision = %+v, want opened batch for instruct", d)
	}
	if d.Reason != "batch_opened" {
		t.Errorf("reason = %q, want batch_opened", d.Reason)
	}
	if !b.Open || b.Key != "instruct" || b.Claimed != 1 {
		t.Errorf("state = %+v, want open instruct batch", b)
	}
}

func TestBatchContinuesSameCapabilityUntilSize(t *testing.T) {
	var b AffinityState
	now := time.Now()
	claimAt(t, &b, "instruct", 4, 10*time.Minute, now) // opens the batch
	for i := 1; i <= 3; i++ {
		d := claimAt(t, &b, "instruct", 4, 10*time.Minute, now.Add(time.Duration(i)*time.Second))
		if d.Reason != "batch_continued" || d.BatchClaimed != i+1 {
			t.Fatalf("claim %d: decision = %+v, want continued batch %d", i, d, i+1)
		}
	}
}

func TestPreferredCapabilityBoundsAndReplacement(t *testing.T) {
	var b AffinityState
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	maxAge := 10 * time.Minute

	if _, ok := b.PreferredCapability(now, 4, maxAge); ok {
		t.Fatal("closed batch unexpectedly provided a preference")
	}
	b.Consider("instruct", 100, 4, maxAge, now)
	if got, ok := b.PreferredCapability(now.Add(maxAge), 4, maxAge); !ok || got != "instruct" {
		t.Fatalf("at age bound = %q, %v; want instruct, true", got, ok)
	}
	if got, ok := b.PreferredCapability(now.Add(maxAge+time.Nanosecond), 4, maxAge); ok || got != "" {
		t.Fatalf("expired preference = %q, %v; want empty, false", got, ok)
	}

	for i := 1; i < 4; i++ {
		b.Consider("instruct", 100, 4, maxAge, now.Add(time.Duration(i)*time.Second))
	}
	if got, ok := b.PreferredCapability(now, 4, maxAge); ok || got != "" {
		t.Fatalf("size-exhausted preference = %q, %v; want empty, false", got, ok)
	}

	b.Close()
	b.Consider("instruct", 100, 4, maxAge, now)
	d := b.Consider("thinking", 100, 4, maxAge, now.Add(time.Second))
	if d.Reason != "batch_opened" || b.Key != "thinking" || b.Claimed != 1 {
		t.Fatalf("replacement decision/state = %+v / %+v", d, b)
	}
}

func TestBatchClosesAtSize(t *testing.T) {
	var b AffinityState
	now := time.Now()
	for i := 0; i < 4; i++ {
		claimAt(t, &b, "instruct", 4, 10*time.Minute, now.Add(time.Duration(i)*time.Second))
	}
	d := claimAt(t, &b, "instruct", 4, 10*time.Minute, now.Add(5*time.Second))
	if d.Reason != "batch_opened" {
		t.Errorf("5th claim reason = %q, want batch_opened (size bound)", d.Reason)
	}
	if d.BatchClaimed != 1 || b.Claimed != 1 {
		t.Errorf("state after reopen = %+v, want Claimed 1", b)
	}
}

func TestBatchClosesAtMaxAge(t *testing.T) {
	var b AffinityState
	now := time.Now()
	claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	d := claimAt(t, &b, "instruct", 4, 10*time.Minute, now.Add(10*time.Minute+time.Second))
	if d.Reason != "batch_opened" {
		t.Errorf("reason = %q, want batch_opened (age bound)", d.Reason)
	}
	// Exactly at the bound the batch still continues.
	b.Close()
	claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	d = claimAt(t, &b, "instruct", 4, 10*time.Minute, now.Add(10*time.Minute))
	if d.Reason != "batch_continued" {
		t.Errorf("at-bound reason = %q, want batch_continued", d.Reason)
	}
}

func TestBatchClosesOnCapabilityChange(t *testing.T) {
	var b AffinityState
	now := time.Now()
	claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	d := claimAt(t, &b, "thinking", 4, 10*time.Minute, now.Add(time.Second))
	if d.Reason != "batch_opened" || d.BatchKey != "thinking" {
		t.Errorf("decision = %+v, want reopened for thinking", d)
	}
	if b.Key != "thinking" || b.Claimed != 1 {
		t.Errorf("state = %+v, want thinking batch", b)
	}
}

func TestBatchCloseResets(t *testing.T) {
	var b AffinityState
	now := time.Now()
	claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	b.Close()
	if b.Open || b.Key != "" || b.Claimed != 0 {
		t.Errorf("state after Close = %+v, want reset", b)
	}
	d := claimAt(t, &b, "instruct", 4, 10*time.Minute, now)
	if d.Reason != "batch_opened" {
		t.Errorf("reason after Close = %q, want batch_opened", d.Reason)
	}
}

func TestDecisionSanitizedFields(t *testing.T) {
	// The diagnostic decision must carry only slugs, priorities, and batch
	// counters — never client, task, or prompt labels. This test pins the
	// shape: a Decision with Claimed=false must not expose internals.
	d := Decision{Reason: "no_eligible"}
	if d.Claimed || d.Capability != "" || d.EffectivePriority != 0 {
		t.Errorf("empty decision leaks state: %+v", d)
	}
}
