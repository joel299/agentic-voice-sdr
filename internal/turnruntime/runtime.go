// Package turnruntime coordinates one provider-agnostic conversation turn.
// It composes canonical domain decisions, policy, orchestration directives, and
// the tool dispatcher without selecting providers or tools itself.
package turnruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
)

var (
	ErrInvalidTurnRuntime          = errors.New("invalid turn runtime input")
	ErrCapabilityContextRequired   = errors.New("capability context is required")
	ErrUnexpectedCapabilityContext = errors.New("unexpected capability context")
	ErrToolDispatchFailed          = errors.New("tool dispatch failed")
)

// CapabilityContext is explicit caller-supplied tool selection and arguments.
// DecisionProvider does not select tools.
type CapabilityContext struct {
	RequestedTool string
	Arguments     map[string]any
	CorrelationID string
}

func (context *CapabilityContext) Validate() error {
	if context == nil || strings.TrimSpace(context.RequestedTool) == "" || context.Arguments == nil {
		return ErrCapabilityContextRequired
	}
	return nil
}

// TurnInput contains only the conversation snapshot and optional tool request
// selected by the caller. It carries no transcript, audio, or provider payload.
type TurnInput struct {
	State      *conversation.ConversationState
	Capability *CapabilityContext
}

// Dispatcher is the narrow dependency needed from the canonical tool runtime.
type Dispatcher interface {
	Dispatch(context.Context, toolruntime.ToolExecutionRequest) (conversation.ToolResult, error)
}

// TurnRuntime composes one decision into one final TurnDirective.
type TurnRuntime struct {
	provider   conversation.DecisionProvider
	dispatcher Dispatcher
}

func New(provider conversation.DecisionProvider, dispatcher Dispatcher) (*TurnRuntime, error) {
	if provider == nil || dispatcher == nil {
		return nil, fmt.Errorf("%w: provider and dispatcher are required", ErrInvalidTurnRuntime)
	}
	return &TurnRuntime{provider: provider, dispatcher: dispatcher}, nil
}

func (runtime *TurnRuntime) ProcessTurn(ctx context.Context, input TurnInput) (conversation.TurnDirective, error) {
	if runtime == nil || runtime.provider == nil || runtime.dispatcher == nil || input.State == nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: runtime and state are required", ErrInvalidTurnRuntime)
	}
	if ctx == nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: context is nil", ErrInvalidTurnRuntime)
	}
	if err := ctx.Err(); err != nil {
		return conversation.TurnDirective{}, err
	}

	decisionInput, err := conversation.NewDecisionInput(input.State)
	if err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: decision input: %v", ErrInvalidTurnRuntime, err)
	}
	decision, err := runtime.provider.Decide(ctx, decisionInput)
	if err != nil {
		return conversation.TurnDirective{}, err
	}
	if err := decision.Validate(); err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: decision: %v", ErrInvalidTurnRuntime, err)
	}

	if decision.NextAction != conversation.ActionRequestCapability {
		if input.Capability != nil {
			return conversation.TurnDirective{}, ErrUnexpectedCapabilityContext
		}
		return conversation.BuildTurnDirective(conversation.OrchestratorInput{
			DecisionInput: decisionInput,
			Decision:      decision,
		})
	}
	if err := input.Capability.Validate(); err != nil {
		return conversation.TurnDirective{}, err
	}

	policyInput, err := conversation.NewToolPolicyInput(input.State, decision, input.Capability.RequestedTool)
	if err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: policy input: %v", ErrInvalidTurnRuntime, err)
	}
	policy, err := conversation.EvaluateToolPolicy(policyInput)
	if err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: policy: %v", ErrInvalidTurnRuntime, err)
	}
	preExecution, err := conversation.BuildTurnDirective(conversation.OrchestratorInput{
		DecisionInput: decisionInput,
		Decision:      decision,
		RequestedTool: input.Capability.RequestedTool,
		Policy:        &policy,
	})
	if err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: pre-execution directive: %v", ErrInvalidTurnRuntime, err)
	}
	if !preExecution.Executable {
		return preExecution, nil
	}

	result, err := runtime.dispatcher.Dispatch(ctx, toolruntime.ToolExecutionRequest{
		Tool:          input.Capability.RequestedTool,
		Arguments:     input.Capability.Arguments,
		CorrelationID: input.Capability.CorrelationID,
		Directive:     preExecution,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return conversation.TurnDirective{}, err
		}
		return conversation.TurnDirective{}, fmt.Errorf("%w: %v", ErrToolDispatchFailed, err)
	}
	return conversation.BuildTurnDirective(conversation.OrchestratorInput{
		DecisionInput: decisionInput,
		Decision:      decision,
		RequestedTool: input.Capability.RequestedTool,
		Policy:        &policy,
		ToolResult:    &result,
	})
}
