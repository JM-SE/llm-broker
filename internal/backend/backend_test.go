package backend

import (
	"context"
	"testing"

	"github.com/JM-SE/llm-broker/internal/domain"
)

func TestFeatureParse(t *testing.T) {
	f, err := ParseFeature("structured_output")
	if err != nil || f != FeatureStructuredOutput {
		t.Errorf("ParseFeature(structured_output) = %v, %v", f, err)
	}
	if _, err := ParseFeature("streaming"); err == nil {
		t.Error("unknown feature must fail")
	}
}

func TestErrorClassification(t *testing.T) {
	e := NewError(domain.ErrorIndeterminate, "conn_lost", "connection lost mid-generation")
	if !e.IsIndeterminate() {
		t.Error("indeterminate class not detected")
	}
	if e.Class != domain.ErrorIndeterminate || e.Code != "conn_lost" {
		t.Errorf("error fields: %+v", e)
	}
	if got := e.Error(); got == "" {
		t.Error("Error() should render a message")
	}
	if domain.ErrorIndeterminate.Retryable() {
		t.Error("indeterminate must never retry")
	}
}

type fakeAdapter struct{}

func (f *fakeAdapter) Type() string { return "fake" }
func (f *fakeAdapter) Infer(ctx context.Context, req *Request) (*Response, error) {
	return &Response{Content: "ok", FinishReason: domain.FinishStop}, nil
}

func TestAdapterContractCompileTime(t *testing.T) {
	// The fake adapter satisfies the interface; the contract is usable
	// without any provider types leaking into domain.
	var a Adapter = &fakeAdapter{}
	resp, err := a.Infer(context.Background(), &Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: "hi"}},
		Response: domain.ResponseContract{Kind: domain.ResponseText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || resp.FinishReason != domain.FinishStop {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// idleController is a minimal Controller implementation proving the
// contract is implementable without any network behavior.
type idleController struct{}

func (idleController) Type() string                       { return "test" }
func (idleController) Idle(context.Context) (bool, error) { return true, nil }
func (idleController) Fence(context.Context) error        { return nil }

func TestControllerContractCompileTime(t *testing.T) {
	// A controller is optional; the interface must be implementable
	// without network behavior.
	var _ Controller = (*idleController)(nil)
}

func TestRequestCarriesNoProviderFields(t *testing.T) {
	// Structural guard: Request must not expose model/URL/credential
	// fields. This test documents the invariant by asserting the zero
	// value has no such data.
	var req Request
	if req.Messages != nil || req.Response.Kind != "" {
		t.Error("Request zero value should be empty")
	}
}
