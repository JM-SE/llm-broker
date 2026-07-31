package openaicompat

import (
	"testing"
)

// FuzzParseChatCompletion asserts that arbitrary provider response bytes can
// never panic and always produce a valid finish reason and sane usage, or a
// parse error. Reasoning content cannot leak by construction: the response
// type has no field for it, and it is dropped before the response is built.
func FuzzParseChatCompletion(f *testing.F) {
	seeds := []string{
		`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"x","reasoning_content":"hidden"}}],"usage":{}}`,
		`{}`,
		`{not json`,
		`{"choices":[]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		resp, err := parseChatCompletion(raw)
		if err != nil {
			return
		}
		if !resp.FinishReason.Valid() {
			t.Errorf("invalid finish reason %q", resp.FinishReason)
		}
		if resp.Usage.TotalTokens < 0 || resp.Usage.PromptTokens < 0 || resp.Usage.CompletionTokens < 0 {
			t.Errorf("negative usage: %+v", resp.Usage)
		}
	})
}
