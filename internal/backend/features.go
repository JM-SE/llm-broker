package backend

import (
	"fmt"
	"strings"
)

// CheckFeatures verifies that a backend instance declares every feature a
// workload requires. Both lists are operator configuration; unknown feature
// names are rejected wherever they appear.
func CheckFeatures(required, declared []string) error {
	for _, d := range declared {
		if _, err := ParseFeature(d); err != nil {
			return err
		}
	}
	for _, r := range required {
		rf, err := ParseFeature(r)
		if err != nil {
			return err
		}
		found := false
		for _, d := range declared {
			if Feature(d) == rf {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("workload requires feature %q but the backend does not declare it (declared: %s)",
				rf, strings.Join(declared, ", "))
		}
	}
	return nil
}
