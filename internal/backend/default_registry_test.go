package backend_test

import (
	"testing"

	// Blank imports wire the built-in adapters into the default registry,
	// exactly as the product binary does at worker startup.
	_ "github.com/JM-SE/llm-broker/internal/backend/fake"
	_ "github.com/JM-SE/llm-broker/internal/backend/openaicompat"

	"github.com/JM-SE/llm-broker/internal/backend"
	"github.com/JM-SE/llm-broker/internal/config"
)

func TestDefaultRegistryHasBuiltins(t *testing.T) {
	names := backend.DefaultRegistry.Names()
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen[config.AdapterFake] || !seen[config.AdapterOpenAICompatible] {
		t.Errorf("default registry missing built-ins: %v", names)
	}
}
