package backend

import (
	"context"
	"fmt"
)

// NoneController is the optional controller for generic deployments that
// cannot prove backend idleness authoritatively. It fails closed: Idle
// never reports idle and Fence always errors, so an ambiguous disconnect
// must be resolved by an operator instead of an automatic reclaim.
type NoneController struct{}

// Type returns the controller type name.
func (NoneController) Type() string { return "none" }

// Idle always reports false: absence of a controller is never evidence
// that an inference stopped.
func (NoneController) Idle(context.Context) (bool, error) { return false, nil }

// Fence always fails: without runtime control the broker cannot render the
// backend unable to start new inferences.
func (NoneController) Fence(context.Context) error {
	return fmt.Errorf("no runtime controller is configured; the backend cannot be fenced programmatically")
}

// CheckNoneController asserts NoneController satisfies the contract.
var _ Controller = NoneController{}
