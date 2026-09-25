// Package toolruntime contains the canonical runtime boundary between an
// authorized conversation directive and a concrete tool provider executor.
package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

var (
	ErrInvalidToolExecutionRequest = errors.New("invalid tool execution request")
	ErrUnknownTool                 = tools.ErrUnknownTool
	ErrToolNotExecutable           = errors.New("tool is not executable")
	ErrToolExecutorNotFound        = errors.New("tool executor not found")
	ErrToolExecutionFailed         = errors.New("tool execution failed")
	ErrInvalidToolExecutionResult  = errors.New("invalid tool execution result")
	ErrDuplicateToolExecutor       = errors.New("tool executor already registered")
)

// ToolExecutionRequest is the minimal runtime payload derived from a validated
// conversation.TurnDirective. It deliberately contains no transcript, audio,
// provider payload, or JEV response.
type ToolExecutionRequest struct {
	Tool          string
	Arguments     map[string]any
	CorrelationID string
	Directive     conversation.TurnDirective
}

func (request ToolExecutionRequest) Validate() error {
	if err := request.Directive.Validate(); err != nil {
		return fmt.Errorf("%w: directive: %v", ErrInvalidToolExecutionRequest, err)
	}
	if request.Directive.Kind != conversation.ActionRequestCapability {
		return fmt.Errorf("%w: directive does not request a capability", ErrInvalidToolExecutionRequest)
	}
	if strings.TrimSpace(request.Tool) == "" || request.Tool != request.Directive.Capability {
		return fmt.Errorf("%w: tool does not match authorized capability", ErrInvalidToolExecutionRequest)
	}
	if request.Directive.CapabilityStatus != conversation.PolicyAllow || !request.Directive.Executable {
		return ErrToolNotExecutable
	}
	if request.Directive.ToolResult != nil {
		return ErrToolNotExecutable
	}
	if request.Arguments == nil {
		return fmt.Errorf("%w: arguments are nil", ErrInvalidToolExecutionRequest)
	}
	return nil
}

// ToolExecutor executes one already-authorized canonical tool request.
type ToolExecutor interface {
	Execute(context.Context, ToolExecutionRequest) (conversation.ToolResult, error)
}

// ExecutorRegistry resolves canonical tool IDs to implementations. Registration
// is synchronized so construction-time and test setup registration are safe;
// Dispatch never mutates the registry and never retries an executor.
type ExecutorRegistry struct {
	mu        sync.RWMutex
	executors map[string]ToolExecutor
}

func NewExecutorRegistry(initial map[string]ToolExecutor) *ExecutorRegistry {
	registry := &ExecutorRegistry{executors: make(map[string]ToolExecutor, len(initial))}
	for name, executor := range initial {
		if strings.TrimSpace(name) != "" && executor != nil {
			registry.executors[name] = executor
		}
	}
	return registry
}

func (registry *ExecutorRegistry) Register(name string, executor ToolExecutor) error {
	if strings.TrimSpace(name) == "" || executor == nil {
		return fmt.Errorf("%w: invalid registration", ErrInvalidToolExecutionRequest)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.executors[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateToolExecutor, name)
	}
	registry.executors[name] = executor
	return nil
}

func (registry *ExecutorRegistry) Resolve(name string) (ToolExecutor, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	executor, ok := registry.executors[name]
	return executor, ok
}

// Dispatcher performs one validation, one resolution, and at most one executor
// invocation for each Dispatch call.
type Dispatcher struct {
	definitions tools.Registry
	executors   *ExecutorRegistry
}

func NewDispatcher(definitions tools.Registry, executors *ExecutorRegistry) *Dispatcher {
	return &Dispatcher{definitions: definitions, executors: executors}
}

func (dispatcher *Dispatcher) Dispatch(ctx context.Context, request ToolExecutionRequest) (conversation.ToolResult, error) {
	if ctx == nil {
		return conversation.ToolResult{}, fmt.Errorf("%w: context is nil", ErrInvalidToolExecutionRequest)
	}
	if err := request.Validate(); err != nil {
		return conversation.ToolResult{}, err
	}
	if dispatcher == nil || dispatcher.definitions == nil || dispatcher.executors == nil {
		return conversation.ToolResult{}, fmt.Errorf("%w: dispatcher is not configured", ErrInvalidToolExecutionRequest)
	}
	if _, err := dispatcher.definitions.Get(request.Tool); err != nil {
		if errors.Is(err, tools.ErrUnknownTool) {
			return conversation.ToolResult{}, fmt.Errorf("%w: %s", tools.ErrUnknownTool, request.Tool)
		}
		return conversation.ToolResult{}, fmt.Errorf("%w: %v", ErrInvalidToolExecutionRequest, err)
	}
	executor, ok := dispatcher.executors.Resolve(request.Tool)
	if !ok {
		return conversation.ToolResult{}, fmt.Errorf("%w: %s", ErrToolExecutorNotFound, request.Tool)
	}
	result, err := executor.Execute(ctx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return conversation.ToolResult{}, err
		}
		return conversation.ToolResult{}, fmt.Errorf("%w: %v", ErrToolExecutionFailed, err)
	}
	if err := result.Validate(); err != nil {
		return conversation.ToolResult{}, fmt.Errorf("%w: %v", ErrInvalidToolExecutionResult, err)
	}
	if result.Tool != request.Tool {
		return conversation.ToolResult{}, fmt.Errorf("%w: result tool does not match requested tool", ErrInvalidToolExecutionResult)
	}
	return result, nil
}
