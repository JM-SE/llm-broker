package domain

import (
	"bytes"
	"testing"
)

func TestGenerationCanonicalEquivalence(t *testing.T) {
	mk := func(stop ...string) Generation {
		var g Generation
		g.MaxTokens = int64Ptr(1024)
		g.Temperature = float64Ptr(0.7)
		g.Stop = stop
		return g
	}
	a, err := mk("END", "END", "\\n", "USER:").Canonical()
	if err != nil {
		t.Fatal(err)
	}
	b, err := mk("USER:", "\\n", "END").Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("canonical differs for equivalent stop sets:\n%s\n%s", a, b)
	}
	// Different values must differ.
	c, _ := mk("END").Canonical()
	if bytes.Equal(a, c) {
		t.Error("canonical must differ for different values")
	}
	// Empty settings render deterministically (empty object).
	d, err := (Generation{}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(d) != "{}" {
		t.Errorf("empty canonical = %q", d)
	}
}

func TestGenerationValidateBounds(t *testing.T) {
	bad := []Generation{
		{MaxTokens: int64Ptr(0)},
		{MaxTokens: int64Ptr(9000)},
		{Temperature: float64Ptr(-0.1)},
		{Temperature: float64Ptr(2.5)},
		{TopP: float64Ptr(1.5)},
		{TopK: int64Ptr(0)},
		{MinP: float64Ptr(-1)},
		{Stop: []string{""}},
		{Stop: []string{string(make([]byte, 65))}},
		{Stop: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
	}
	for i, g := range bad {
		if err := g.Validate(); err == nil {
			t.Errorf("case %d should fail: %+v", i, g)
		}
	}
	good := Generation{MaxTokens: int64Ptr(1), Temperature: float64Ptr(0), TopP: float64Ptr(1),
		TopK: int64Ptr(100), MinP: float64Ptr(0), Seed: int64Ptr(-5), Stop: []string{"a"}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid generation rejected: %v", err)
	}
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }
