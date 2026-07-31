package domain

import (
	"strings"
	"testing"
)

func TestValidateSlugValid(t *testing.T) {
	valid := []string{"a", "z", "0", "default", "job-search", "job_search", "cap.1",
		"a0", strings.Repeat("a", 64), "instruct", "thinking-v2", "2026.q3"}
	for _, s := range valid {
		if err := ValidateSlug("test", s); err != nil {
			t.Errorf("ValidateSlug(%q): %v", s, err)
		}
	}
}

func TestValidateSlugInvalid(t *testing.T) {
	invalid := []string{"", "-lead", "trail-", ".dot", "UPPER", "with space",
		"with/slash", strings.Repeat("a", 65), "a-b-", "-", "..", "é"}
	for _, s := range invalid {
		if err := ValidateSlug("test", s); err == nil {
			t.Errorf("ValidateSlug(%q) should fail", s)
		}
	}
}

func TestValidateSlugNamesKind(t *testing.T) {
	err := ValidateSlug("workload", "UPPER")
	if err == nil || !strings.Contains(err.Error(), "workload") {
		t.Fatalf("error should name the kind: %v", err)
	}
}
