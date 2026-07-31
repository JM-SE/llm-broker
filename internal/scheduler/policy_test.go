package scheduler

import (
	"testing"
	"time"

	"github.com/JM-SE/llm-broker/internal/domain"
)

func TestEffectivePriorityDelegatesToDomain(t *testing.T) {
	ages := []time.Duration{
		0, 30 * time.Second, 10 * time.Minute, 20 * time.Minute,
		45 * time.Minute, 3 * time.Hour,
	}
	classes := []domain.PriorityClass{
		domain.PriorityHigh, domain.PriorityNormal, domain.PriorityBatch,
	}
	for _, class := range classes {
		for _, age := range ages {
			want := domain.EffectivePriority(class, int64(age.Seconds()))
			if got := EffectivePriority(class, age); got != want {
				t.Errorf("EffectivePriority(%s, %v) = %d, want %d", class, age, got, want)
			}
		}
	}
}

func TestEffectivePriorityBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		class domain.PriorityClass
		age   time.Duration
		want  int
	}{
		{"fresh high", domain.PriorityHigh, 0, 300},
		{"aged high never ages", domain.PriorityHigh, 3 * time.Hour, 300},
		{"fresh normal", domain.PriorityNormal, 0, 200},
		{"normal one step", domain.PriorityNormal, 10 * time.Minute, 300},
		{"normal capped at high base", domain.PriorityNormal, 3 * time.Hour, 300},
		{"fresh batch", domain.PriorityBatch, 0, 100},
		{"batch one step", domain.PriorityBatch, 10 * time.Minute, 200},
		{"batch full aging", domain.PriorityBatch, 30 * time.Minute, 400},
		{"batch capped at 400", domain.PriorityBatch, 3 * time.Hour, 400},
		{"no partial steps", domain.PriorityBatch, 9*time.Minute + 59*time.Second, 100},
	}
	for _, c := range cases {
		if got := EffectivePriority(c.class, c.age); got != c.want {
			t.Errorf("%s: EffectivePriority = %d, want %d", c.name, got, c.want)
		}
	}
}
