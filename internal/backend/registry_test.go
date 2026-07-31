package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/JM-SE/llm-broker/internal/config"
)

type stubAdapter struct{}

func (stubAdapter) Type() string { return "stub" }
func (stubAdapter) Infer(context.Context, *Request) (*Response, error) {
	return &Response{}, nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register("stub", func(config.BackendConfig) (Adapter, error) { return stubAdapter{}, nil })

	t.Run("lookup and construct", func(t *testing.T) {
		a, err := r.New(config.BackendConfig{Type: "stub"})
		if err != nil {
			t.Fatal(err)
		}
		if a.Type() != "stub" {
			t.Errorf("Type() = %q", a.Type())
		}
	})
	t.Run("unknown type", func(t *testing.T) {
		if _, err := r.New(config.BackendConfig{Type: "nope"}); err == nil ||
			!strings.Contains(err.Error(), "nope") {
			t.Errorf("error = %v, want unknown type mention", err)
		}
	})
	t.Run("duplicate panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("duplicate registration must panic")
			}
		}()
		r.Register("stub", func(config.BackendConfig) (Adapter, error) { return stubAdapter{}, nil })
	})
	t.Run("empty name panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("empty registration must panic")
			}
		}()
		r.Register("", func(config.BackendConfig) (Adapter, error) { return stubAdapter{}, nil })
	})
}

func TestCheckFeatures(t *testing.T) {
	if err := CheckFeatures(nil, nil); err != nil {
		t.Errorf("empty check must pass: %v", err)
	}
	if err := CheckFeatures([]string{"structured_output"}, []string{"structured_output"}); err != nil {
		t.Errorf("declared feature must pass: %v", err)
	}
	if err := CheckFeatures([]string{"structured_output"}, nil); err == nil {
		t.Error("required but undeclared feature must fail")
	}
	if err := CheckFeatures([]string{"structured_output"}, []string{"structured_output", "bogus_feature"}); err == nil {
		t.Error("unknown declared feature must fail")
	}
	if err := CheckFeatures([]string{"bogus_required"}, nil); err == nil {
		t.Error("unknown required feature must fail")
	}
}

func TestNoneControllerFailsClosed(t *testing.T) {
	c := NoneController{}
	if c.Type() != "none" {
		t.Errorf("Type() = %q", c.Type())
	}
	idle, err := c.Idle(context.Background())
	if err != nil || idle {
		t.Errorf("Idle = %v, %v; want false, nil (never provably idle)", idle, err)
	}
	if err := c.Fence(context.Background()); err == nil {
		t.Error("Fence must fail without runtime control")
	}
}
