package toolruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

type countingExecutor struct {
	mu       sync.Mutex
	calls    int
	seenCtx  context.Context
	seenTool string
	result   conversation.ToolResult
	err      error
}

func (e *countingExecutor) Execute(ctx context.Context, request ToolExecutionRequest) (conversation.ToolResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.seenCtx = ctx
	e.seenTool = request.Tool
	if e.err != nil {
		return conversation.ToolResult{}, e.err
	}
	return e.result, nil
}

func (e *countingExecutor) Calls() int { e.mu.Lock(); defer e.mu.Unlock(); return e.calls }

func sampleToolRegistry(t *testing.T, names ...string) tools.Registry {
	t.Helper()
	registry := tools.NewInMemoryRegistry()
	for _, name := range names {
		if err := registry.Register(sampleDefinition(name)); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func sampleDefinition(name string) tools.ToolDefinition {
	return tools.ToolDefinition{
		Name: name, Description: "test tool", InputSchema: tools.SchemaDefinition{Type: "object"}, OutputSchema: tools.SchemaDefinition{Type: "object"}, AllowedContexts: []string{"test"}, Timeout: 1,
		RetryPolicy: &tools.RetryPolicyMetadata{}, IdempotencyStrategy: "caller",
		AuditPolicy:     tools.AuditPolicy{AuditLevel: "standard"},
		ProviderAdapter: tools.ProviderAdapterIdentifier{ProviderName: "test", AdapterType: "fake"},
	}
}

func allowedDirective(t *testing.T, name string) conversation.TurnDirective {
	t.Helper()
	return conversation.TurnDirective{Kind: conversation.ActionRequestCapability, Reason: conversation.ReasonCapabilityRequired, Capability: name, CapabilityStatus: conversation.PolicyAllow, Executable: true}
}

func request(t *testing.T, name string) ToolExecutionRequest {
	t.Helper()
	return ToolExecutionRequest{Tool: name, Directive: allowedDirective(t, name), Arguments: map[string]any{"value": "ok"}, CorrelationID: "corr-1"}
}

func TestDispatcherAllowedExecutableRunsExactlyOnce(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	executor := &countingExecutor{result: conversation.ToolResult{Tool: name, Status: conversation.ToolResultSucceeded}}
	dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))

	got, err := dispatcher.Dispatch(context.Background(), request(t, name))
	if err != nil || got != executor.result {
		t.Fatalf("result=%+v error=%v", got, err)
	}
	if executor.Calls() != 1 {
		t.Fatalf("calls=%d want 1", executor.Calls())
	}
}

func TestDispatcherRejectsDeniedDeferredAndNonExecutableWithoutCallingExecutor(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	for _, tc := range []struct {
		name   string
		status conversation.PolicyStatus
		exec   bool
		want   error
	}{
		{"denied", conversation.PolicyDeny, false, ErrToolNotExecutable},
		{"deferred", conversation.PolicyDefer, false, ErrToolNotExecutable},
		{"allow-not-executable", conversation.PolicyAllow, false, ErrInvalidToolExecutionRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &countingExecutor{result: conversation.ToolResult{Tool: name, Status: conversation.ToolResultSucceeded}}
			dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))
			req := request(t, name)
			req.Directive.CapabilityStatus, req.Directive.Executable = tc.status, tc.exec
			_, err := dispatcher.Dispatch(context.Background(), req)
			if !errors.Is(err, tc.want) || executor.Calls() != 0 {
				t.Fatalf("error=%v calls=%d", err, executor.Calls())
			}
		})
	}
}

func TestDispatcherRejectsUnknownAndKnownUnregisteredTools(t *testing.T) {
	known := tools.ToolCalendarCheckAvailability
	dispatcher := NewDispatcher(sampleToolRegistry(t, known), NewExecutorRegistry(nil))
	_, err := dispatcher.Dispatch(context.Background(), request(t, "unknown.tool"))
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown error=%v", err)
	}
	req := request(t, known)
	_, err = dispatcher.Dispatch(context.Background(), req)
	if !errors.Is(err, ErrToolExecutorNotFound) {
		t.Fatalf("unregistered error=%v", err)
	}
}

func TestDispatcherRejectsMismatchedAndInvalidResults(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	for _, tc := range []struct {
		name   string
		result conversation.ToolResult
		want   error
	}{
		{"mismatch", conversation.ToolResult{Tool: tools.ToolCalendarCreateEvent, Status: conversation.ToolResultSucceeded}, ErrInvalidToolExecutionResult},
		{"invalid", conversation.ToolResult{Tool: "", Status: conversation.ToolResultSucceeded}, ErrInvalidToolExecutionResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &countingExecutor{result: tc.result}
			dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))
			_, err := dispatcher.Dispatch(context.Background(), request(t, name))
			if !errors.Is(err, tc.want) || executor.Calls() != 1 {
				t.Fatalf("error=%v calls=%d", err, executor.Calls())
			}
		})
	}
}

func TestDispatcherClassifiesExecutorErrorWithoutRetry(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	executor := &countingExecutor{err: errors.New("provider failure")}
	dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))
	_, err := dispatcher.Dispatch(context.Background(), request(t, name))
	if !errors.Is(err, ErrToolExecutionFailed) || executor.Calls() != 1 {
		t.Fatalf("error=%v calls=%d", err, executor.Calls())
	}
}

func TestDispatcherPropagatesCallerCancellation(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &countingExecutor{err: context.Canceled}
	dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))
	_, err := dispatcher.Dispatch(ctx, request(t, name))
	if !errors.Is(err, context.Canceled) || executor.Calls() != 1 || executor.seenCtx != ctx {
		t.Fatalf("error=%v calls=%d same_ctx=%v", err, executor.Calls(), executor.seenCtx == ctx)
	}
}

func TestExecutorRegistryRejectsDuplicateRegistration(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	a, b := &countingExecutor{}, &countingExecutor{}
	registry := NewExecutorRegistry(map[string]ToolExecutor{name: a})
	if err := registry.Register(name, b); !errors.Is(err, ErrDuplicateToolExecutor) {
		t.Fatalf("error=%v", err)
	}
}

func TestDispatcherRejectsInvalidDirectiveBeforeExecution(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	executor := &countingExecutor{result: conversation.ToolResult{Tool: name, Status: conversation.ToolResultSucceeded}}
	dispatcher := NewDispatcher(sampleToolRegistry(t, name), NewExecutorRegistry(map[string]ToolExecutor{name: executor}))
	req := request(t, name)
	req.Directive.Capability = tools.ToolCalendarCreateEvent
	_, err := dispatcher.Dispatch(context.Background(), req)
	if !errors.Is(err, ErrInvalidToolExecutionRequest) || executor.Calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, executor.Calls())
	}
}
