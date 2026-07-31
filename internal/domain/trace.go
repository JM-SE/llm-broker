package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Trace is explicit, bounded, non-sensitive correlation metadata supplied
// by the client (for example a job or run identifier). It is never used
// for authentication or authorization decisions, and it must not contain
// prompt or personal data.
type Trace map[string]string

// Validate checks the cardinality, size, and shape of trace metadata.
func (t Trace) Validate() error {
	if len(t) == 0 {
		return nil
	}
	if len(t) > MaxTraceKeys {
		return fmt.Errorf("trace has %d keys, max %d", len(t), MaxTraceKeys)
	}
	total := 0
	for k, v := range t {
		if err := ValidateSlug("trace key", k); err != nil {
			return err
		}
		if len(v) > MaxTraceValueBytes {
			return fmt.Errorf("trace value for %q has %d bytes, max %d", k, len(v), MaxTraceValueBytes)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("trace value for %q is not valid UTF-8", k)
		}
		if strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("trace value for %q contains a NUL byte", k)
		}
		total += len(v)
	}
	if total > MaxTraceBytes {
		return fmt.Errorf("trace metadata totals %d bytes, max %d", total, MaxTraceBytes)
	}
	return nil
}
