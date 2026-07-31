package domain

import "fmt"

// FinishReason is the stable output-completion vocabulary reported by
// adapters after normalization.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishContentFilter FinishReason = "content_filter"
	FinishUnknown       FinishReason = "unknown"
)

// Valid reports whether the reason is part of the stable vocabulary.
func (r FinishReason) Valid() bool {
	switch r {
	case FinishStop, FinishLength, FinishContentFilter, FinishUnknown:
		return true
	default:
		return false
	}
}

// Usage reports token accounting for a completed inference.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Result is the normalized inference result returned to clients.
//
// Deliberately absent: any reasoning field. Hidden reasoning content is
// discarded by adapters and never persisted, logged, or returned.
type Result struct {
	Content      string       `json:"content"`
	FinishReason FinishReason `json:"finish_reason"`
	Usage        Usage        `json:"usage"`
}

// Validate checks result shape before storage/return.
func (r Result) Validate() error {
	if !r.FinishReason.Valid() {
		return fmt.Errorf("invalid finish_reason %q", r.FinishReason)
	}
	if r.Usage.PromptTokens < 0 || r.Usage.CompletionTokens < 0 || r.Usage.TotalTokens < 0 {
		return fmt.Errorf("usage must not be negative")
	}
	return nil
}
