// Package turnruntime coordinates one provider-agnostic conversation turn.
// It composes canonical domain decisions, policy, orchestration directives, and
// the tool dispatcher without selecting providers or tools itself.
package turnruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/salesintent"
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
	observe    func(stage, outcome string)
}

// StageError identifies the provider-neutral turn-processing boundary that
// failed. Its message intentionally omits provider response text.
type StageError struct {
	Stage string
	Cause error
}

func (e *StageError) Error() string   { return "turn runtime stage failed: " + e.Stage }
func (e *StageError) Unwrap() error   { return e.Cause }
func (e *StageError) AIStage() string { return e.Stage }

func (runtime *TurnRuntime) SetStageObserver(observer func(stage, outcome string)) {
	if runtime != nil {
		runtime.observe = observer
	}
}

// ReportStage lets adjacent orchestration owners report fixed milestones
// through the same call-scoped observer without exposing the observer itself.
func (runtime *TurnRuntime) ReportStage(stage, outcome string) {
	runtime.stage(stage, outcome)
}

func (runtime *TurnRuntime) stage(stage, outcome string) {
	if runtime.observe != nil {
		runtime.observe(stage, outcome)
	}
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
	turns := input.State.Turns()
	if len(turns) > 0 {
		last := turns[len(turns)-1]
		if last.Role == conversation.RoleLead && last.Transcript == conversation.TranscriptFinal && salesintent.HasExplicitOptOut(last.Text) {
			if err := input.State.RecordSignal(conversation.SignalOptedOut); err != nil {
				return conversation.TurnDirective{}, err
			}
		}
	}

	decisionInput, err := conversation.NewDecisionInput(input.State)
	if err != nil {
		return conversation.TurnDirective{}, fmt.Errorf("%w: decision input: %v", ErrInvalidTurnRuntime, err)
	}
	decisionInput.HasMatchingExecutableCapability = input.Capability != nil && input.Capability.Validate() == nil
	telemetry.MarkTurn(ctx, "jev_started_at")
	runtime.stage("jev_provider", "started")
	decision, err := runtime.provider.Decide(ctx, decisionInput)
	telemetry.MarkTurn(ctx, "jev_completed_at")
	if err != nil {
		runtime.stage("jev_provider", "failed")
		return conversation.TurnDirective{}, &StageError{Stage: "jev_provider", Cause: err}
	}
	runtime.stage("jev_provider", "completed")
	if err := decision.Validate(); err != nil {
		return conversation.TurnDirective{}, &StageError{Stage: "turn_directive", Cause: fmt.Errorf("%w: decision: %v", ErrInvalidTurnRuntime, err)}
	}

	if decision.NextAction != conversation.ActionRequestCapability {
		if input.Capability != nil {
			return conversation.TurnDirective{}, ErrUnexpectedCapabilityContext
		}
		directive, err := conversation.BuildTurnDirective(conversation.OrchestratorInput{
			DecisionInput: decisionInput,
			Decision:      decision,
		})
		if err != nil {
			return conversation.TurnDirective{}, &StageError{Stage: "turn_directive", Cause: err}
		}
		runtime.stage("turn_directive", "created")
		return directive, nil
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
	if err := ctx.Err(); err != nil {
		return conversation.TurnDirective{}, err
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
	directive, err := conversation.BuildTurnDirective(conversation.OrchestratorInput{
		DecisionInput: decisionInput,
		Decision:      decision,
		RequestedTool: input.Capability.RequestedTool,
		Policy:        &policy,
		ToolResult:    &result,
	})
	if err != nil {
		return conversation.TurnDirective{}, &StageError{Stage: "turn_directive", Cause: err}
	}
	runtime.stage("turn_directive", "created")
	return directive, nil
}
