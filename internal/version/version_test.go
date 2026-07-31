package version

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	s := String()
	if !strings.Contains(s, "llm-broker") {
		t.Errorf("String() missing program name: %s", s)
	}
	if !strings.Contains(s, Version) {
		t.Errorf("String() missing version: %s", s)
	}
}

func TestShort(t *testing.T) {
	s := Short()
	if !strings.Contains(s, "llm-broker") {
		t.Errorf("Short() missing program name: %s", s)
	}
	if !strings.Contains(s, Version) {
		t.Errorf("Short() missing version: %s", s)
	}
}
