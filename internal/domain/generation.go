package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Generation is the normalized generation vocabulary. The field set is a
// frozen product contract; provider-specific parameters are out of scope
// for v1. Pointers distinguish "absent" from zero values so the canonical
// representation is exact.
type Generation struct {
	MaxTokens   *int64   `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int64   `json:"top_k,omitempty"`
	MinP        *float64 `json:"min_p,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

// Intrinsic value bounds of the vocabulary. Workload policies may narrow
// them further per deployment.
const (
	MaxTokensMin   = 1
	MaxTokensMax   = MaxOutputTokens
	TemperatureMin = 0.0
	TemperatureMax = 2.0
	TopPMin        = 0.0
	TopPMax        = 1.0
	TopKMin        = 1
	TopKMax        = 100
	MinPMin        = 0.0
	MinPMax        = 1.0
	MaxStopItems   = 8
	MaxStopLen     = 64
)

// Validate checks the intrinsic bounds of the vocabulary. Policy-level
// restrictions (allowed fields, narrower ranges) are applied by the
// workload policy during submission validation.
func (g *Generation) Validate() error {
	if g == nil {
		return nil
	}
	if g.MaxTokens != nil {
		if *g.MaxTokens < MaxTokensMin || *g.MaxTokens > MaxTokensMax {
			return fmt.Errorf("max_tokens %d outside [%d, %d]", *g.MaxTokens, MaxTokensMin, MaxTokensMax)
		}
	}
	if g.Temperature != nil {
		if *g.Temperature < TemperatureMin || *g.Temperature > TemperatureMax {
			return fmt.Errorf("temperature %v outside [%v, %v]", *g.Temperature, TemperatureMin, TemperatureMax)
		}
	}
	if g.TopP != nil {
		if *g.TopP < TopPMin || *g.TopP > TopPMax {
			return fmt.Errorf("top_p %v outside [%v, %v]", *g.TopP, TopPMin, TopPMax)
		}
	}
	if g.TopK != nil {
		if *g.TopK < TopKMin || *g.TopK > TopKMax {
			return fmt.Errorf("top_k %d outside [%d, %d]", *g.TopK, TopKMin, TopKMax)
		}
	}
	if g.MinP != nil {
		if *g.MinP < MinPMin || *g.MinP > MinPMax {
			return fmt.Errorf("min_p %v outside [%v, %v]", *g.MinP, MinPMin, MinPMax)
		}
	}
	if len(g.Stop) > MaxStopItems {
		return fmt.Errorf("stop has %d items, max %d", len(g.Stop), MaxStopItems)
	}
	for i, s := range g.Stop {
		if len(s) == 0 || len(s) > MaxStopLen {
			return fmt.Errorf("stop[%d] length %d outside [1, %d]", i, len(s), MaxStopLen)
		}
		if !utf8.ValidString(s) {
			return fmt.Errorf("stop[%d] is not valid UTF-8", i)
		}
	}
	return nil
}

// NormalizedStop returns the stop tokens with duplicates removed and sorted.
// Stop tokens form a set semantically; sorting makes the canonical form
// independent of client ordering.
func (g *Generation) NormalizedStop() []string {
	if len(g.Stop) < 2 {
		if len(g.Stop) == 0 {
			return nil
		}
		return []string{g.Stop[0]}
	}
	seen := make(map[string]bool, len(g.Stop))
	out := make([]string, 0, len(g.Stop))
	for _, s := range g.Stop {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Canonical renders the normalized, deterministic JSON bytes of the
// generation settings. Equivalent settings (differing key order, stop
// ordering, duplicate stop tokens) always produce identical bytes.
func (g Generation) Canonical() ([]byte, error) {
	canon := struct {
		MaxTokens   *int64   `json:"max_tokens,omitempty"`
		Temperature *float64 `json:"temperature,omitempty"`
		TopP        *float64 `json:"top_p,omitempty"`
		TopK        *int64   `json:"top_k,omitempty"`
		MinP        *float64 `json:"min_p,omitempty"`
		Seed        *int64   `json:"seed,omitempty"`
		Stop        []string `json:"stop,omitempty"`
	}{
		MaxTokens:   g.MaxTokens,
		Temperature: g.Temperature,
		TopP:        g.TopP,
		TopK:        g.TopK,
		MinP:        g.MinP,
		Seed:        g.Seed,
		Stop:        g.NormalizedStop(),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&canon); err != nil {
		return nil, fmt.Errorf("canonicalize generation: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// GenerationFieldNames lists the stable vocabulary in canonical order.
var GenerationFieldNames = []string{
	"max_tokens", "temperature", "top_p", "top_k", "min_p", "seed", "stop",
}

// IsGenerationField reports whether name is part of the vocabulary.
func IsGenerationField(name string) bool {
	for _, f := range GenerationFieldNames {
		if f == name {
			return true
		}
	}
	return false
}

// String renders a compact normalized description (for diagnostics only;
// never includes prompts).
func (g *Generation) String() string {
	if g == nil {
		return "generation:{}"
	}
	var parts []string
	if g.MaxTokens != nil {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", *g.MaxTokens))
	}
	if g.Temperature != nil {
		parts = append(parts, fmt.Sprintf("temperature=%v", *g.Temperature))
	}
	if g.TopP != nil {
		parts = append(parts, fmt.Sprintf("top_p=%v", *g.TopP))
	}
	if g.TopK != nil {
		parts = append(parts, fmt.Sprintf("top_k=%d", *g.TopK))
	}
	if g.MinP != nil {
		parts = append(parts, fmt.Sprintf("min_p=%v", *g.MinP))
	}
	if g.Seed != nil {
		parts = append(parts, fmt.Sprintf("seed=%d", *g.Seed))
	}
	if len(g.Stop) > 0 {
		parts = append(parts, fmt.Sprintf("stop=%d", len(g.Stop)))
	}
	return "generation:{" + strings.Join(parts, " ") + "}"
}
