package backend

import (
	"fmt"
	"sync"

	"github.com/JM-SE/llm-broker/internal/config"
)

// Factory constructs an adapter from operator configuration. Factories are
// inert: no network, no environment reads beyond documented secret lookup,
// and no persistence access.
type Factory func(cfg config.BackendConfig) (Adapter, error)

// Registry maps adapter type names to their compile-time factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory under a type name. Duplicate names panic: adapter
// registration is a compile-time, single-owner operation.
func (r *Registry) Register(typeName string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if typeName == "" {
		panic("backend: register with empty type name")
	}
	if f == nil {
		panic(fmt.Sprintf("backend: register %q with nil factory", typeName))
	}
	if _, ok := r.factories[typeName]; ok {
		panic(fmt.Sprintf("backend: duplicate registration %q", typeName))
	}
	r.factories[typeName] = f
}

// Lookup returns the factory for a type name.
func (r *Registry) Lookup(typeName string) (Factory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[typeName]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for type %q", typeName)
	}
	return f, nil
}

// New constructs an adapter for the given backend configuration.
func (r *Registry) New(cfg config.BackendConfig) (Adapter, error) {
	f, err := r.Lookup(cfg.Type)
	if err != nil {
		return nil, err
	}
	return f(cfg)
}

// Names returns the registered type names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// DefaultRegistry is the process-wide registry populated by init() of the
// built-in adapter packages (compile-time registration).
var DefaultRegistry = NewRegistry()

// RegisterDefault registers a factory in the default registry.
func RegisterDefault(typeName string, f Factory) {
	DefaultRegistry.Register(typeName, f)
}
