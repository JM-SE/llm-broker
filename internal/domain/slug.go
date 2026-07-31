package domain

import (
	"fmt"
	"regexp"
)

// MaxSlugLen bounds all stable identifier slugs (workloads, capabilities,
// clients, backends, features, trace keys).
const MaxSlugLen = 64

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// ValidateSlug checks a stable identifier: 1-64 chars of lowercase letters,
// digits, dot, underscore, or dash; starts and ends with a letter or digit.
func ValidateSlug(kind, s string) error {
	if len(s) == 0 {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if len(s) > MaxSlugLen {
		return fmt.Errorf("%s %q exceeds %d characters", kind, s, MaxSlugLen)
	}
	if !slugRe.MatchString(s) {
		return fmt.Errorf("%s %q is not a valid slug (lowercase letters, digits, '.', '_', '-'; starts and ends with a letter or digit)", kind, s)
	}
	return nil
}
