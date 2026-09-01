package policy

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JM-SE/llm-broker/internal/domain"
)

// generationConstraint is the durable JSON shape for one allowed generation
// field. It intentionally lives in policy rather than config or postgres so
// runtime authorization uses the same pure rules as policy projection.
type generationConstraint struct {
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	MaxItems *int     `json:"max_items,omitempty"`
}

// ValidateGeneration applies the durable workload generation policy. The
// caller's field presence is evaluated before values: explicit null and empty
// collections are supplied fields, not omissions. An absent allowlist
// therefore denies every supplied override.
func ValidateGeneration(g domain.Generation, allowedJSON []byte, maxOutputTokens int) error {
	allowed := make(map[string]generationConstraint)
	if len(allowedJSON) > 0 && string(allowedJSON) != "null" && string(allowedJSON) != "{}" {
		if err := json.Unmarshal(allowedJSON, &allowed); err != nil {
			return errors.New("workload policy data invalid")
		}
	}
	for field := range allowed {
		if !domain.IsGenerationField(field) {
			return errors.New("workload policy data invalid")
		}
	}

	for _, field := range g.PresentFields() {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("generation field %s is not allowed by the workload policy", field)
		}
	}

	if maxOutputTokens > 0 && g.MaxTokens != nil && *g.MaxTokens > int64(maxOutputTokens) {
		return fmt.Errorf("max_tokens %d exceeds the workload limit %d", *g.MaxTokens, maxOutputTokens)
	}

	for field, constraint := range allowed {
		switch field {
		case "max_tokens":
			if g.MaxTokens != nil {
				if err := checkNumber("max_tokens", float64(*g.MaxTokens), constraint); err != nil {
					return err
				}
			}
		case "temperature":
			if g.Temperature != nil {
				if err := checkNumber("temperature", *g.Temperature, constraint); err != nil {
					return err
				}
			}
		case "top_p":
			if g.TopP != nil {
				if err := checkNumber("top_p", *g.TopP, constraint); err != nil {
					return err
				}
			}
		case "top_k":
			if g.TopK != nil {
				if err := checkNumber("top_k", float64(*g.TopK), constraint); err != nil {
					return err
				}
			}
		case "min_p":
			if g.MinP != nil {
				if err := checkNumber("min_p", *g.MinP, constraint); err != nil {
					return err
				}
			}
		case "seed":
			if g.Seed != nil {
				if err := checkNumber("seed", float64(*g.Seed), constraint); err != nil {
					return err
				}
			}
		case "stop":
			if g.Stop != nil && constraint.MaxItems != nil && len(g.Stop) > *constraint.MaxItems {
				return fmt.Errorf("stop has %d entries, workload maximum %d", len(g.Stop), *constraint.MaxItems)
			}
		}
	}
	return nil
}

func checkNumber(field string, value float64, constraint generationConstraint) error {
	if constraint.Min != nil && value < *constraint.Min {
		return fmt.Errorf("%s %v is below the workload minimum %v", field, value, *constraint.Min)
	}
	if constraint.Max != nil && value > *constraint.Max {
		return fmt.Errorf("%s %v exceeds the workload maximum %v", field, value, *constraint.Max)
	}
	return nil
}
