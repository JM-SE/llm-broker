package config

import (
	"strings"
	"testing"
)

// FuzzDecode ensures the strict config decoder never panics and never
// accepts malformed documents.
func FuzzDecode(f *testing.F) {
	seeds := []string{
		validConfigJSON(),
		`{}`,
		`{"version": 99}`,
		`not json`,
		`{"version": 1, "backends": {"b": {"type": "fake"}}}`,
		`{"version": 1, "backends": {"b": {"type": "bogus"}}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		cfg, err := Decode(strings.NewReader(data))
		if err != nil {
			return
		}
		// Whatever was accepted must survive redaction and re-encode.
		if _, err := cfg.RedactedJSON(); err != nil {
			t.Fatalf("accepted config not redactable: %v", err)
		}
		if cfg.Version != Version {
			t.Fatalf("accepted config with version %d", cfg.Version)
		}
	})
}
