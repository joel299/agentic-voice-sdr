package tools

import (
	"fmt"
	"sort"
	"sync"
)

type Registry interface {
	Register(def ToolDefinition) error
	Get(name string) (ToolDefinition, error)
	List() []ToolDefinition
	IsAllowedInContext(name string, context string) bool
	ValidateAllowedInContext(name string, context string) error
}

type InMemoryRegistry struct {
	mu    sync.RWMutex
	tools map[string]ToolDefinition
}

func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		tools: make(map[string]ToolDefinition),
	}
}

func (r *InMemoryRegistry) Register(def ToolDefinition) error {
	if err := def.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, def.Name)
	}
	r.tools[def.Name] = def.Clone()
	return nil
}

func (r *InMemoryRegistry) Get(name string) (ToolDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, exists := r.tools[name]
	if !exists {
		return ToolDefinition{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	return def.Clone(), nil
}

func (r *InMemoryRegistry) List() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]ToolDefinition, 0, len(r.tools))
	for _, def := range r.tools {
		list = append(list, def.Clone())
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

func (r *InMemoryRegistry) IsAllowedInContext(name string, context string) bool {
	return r.ValidateAllowedInContext(name, context) == nil
}

func (r *InMemoryRegistry) ValidateAllowedInContext(name string, context string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, exists := r.tools[name]
	if !exists {
		return fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	for _, ctx := range def.AllowedContexts {
		if ctx == context {
			return nil
		}
	}
	return fmt.Errorf("%w: tool %s is not allowed in context %s", ErrToolNotAllowedInContext, name, context)
}
