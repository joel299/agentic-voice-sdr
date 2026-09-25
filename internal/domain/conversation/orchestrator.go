package conversation

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidOrchestratorInput = errors.New("invalid conversation orchestrator input")
	ErrInvalidTurnDirective     = errors.New("invalid turn directive")
	ErrInvalidToolResult        = errors.New("invalid tool result")
)

type ToolResultStatus string

const (
	ToolResultSucceeded ToolResultStatus = "succeeded"
	ToolResultFailed    ToolResultStatus = "failed"
)

type ToolResult struct {
	Tool   string
	Status ToolResultStatus
}

func (result ToolResult) Validate() error {
	if strings.TrimSpace(result.Tool) == "" {
		return fmt.Errorf("%w: tool is empty", ErrInvalidToolResult)
	}
	switch result.Status {
	case ToolResultSucceeded, ToolResultFailed:
		return nil
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidToolResult, result.Status)
	}
}

type OrchestratorInput struct {
	DecisionInput DecisionInput
	Decision      Decision
	RequestedTool string
	Policy        *ToolPolicyResult
	ToolResult    *ToolResult
}

func (input OrchestratorInput) Validate() error {
	if err := input.DecisionInput.validate(); err != nil {
		return fmt.Errorf("%w: decision input: %v", ErrInvalidOrchestratorInput, err)
	}
	if err := input.Decision.Validate(); err != nil {
		return fmt.Errorf("%w: decision: %v", ErrInvalidOrchestratorInput, err)
	}
	if input.Policy != nil {
		if err := input.Policy.Validate(); err != nil {
			return fmt.Errorf("%w: policy result: %v", ErrInvalidOrchestratorInput, err)
		}
	}

	isCapability := input.Decision.NextAction == ActionRequestCapability
	if isCapability {
		if strings.TrimSpace(input.RequestedTool) == "" {
			return fmt.Errorf("%w: capability request requires a tool", ErrInvalidOrchestratorInput)
		}
		if input.Policy == nil {
			return fmt.Errorf("%w: capability request requires a policy result", ErrInvalidOrchestratorInput)
		}
		if err := input.validatePolicyBinding(); err != nil {
			return err
		}
	} else if input.RequestedTool != "" || input.ToolResult != nil {
		return fmt.Errorf("%w: non-capability decision cannot carry tool context", ErrInvalidOrchestratorInput)
	}

	if input.ToolResult != nil {
		if err := input.ToolResult.Validate(); err != nil {
			return fmt.Errorf("%w: tool result: %v", ErrInvalidOrchestratorInput, err)
		}
		if input.ToolResult.Tool != input.RequestedTool {
			return fmt.Errorf("%w: tool result does not match requested tool", ErrInvalidOrchestratorInput)
		}
		if input.Policy.Status != PolicyAllow {
			return fmt.Errorf("%w: tool result requires an allowed policy", ErrInvalidOrchestratorInput)
		}
	}
	return nil
}

func (input OrchestratorInput) validatePolicyBinding() error {
	canonicalInput := ToolPolicyInput{
		DecisionInput: input.DecisionInput,
		Decision:      input.Decision,
		RequestedTool: input.RequestedTool,
		Effect:        classifyToolEffect(input.RequestedTool),
	}
	canonical, err := EvaluateToolPolicy(canonicalInput)
	if err != nil {
		return fmt.Errorf("%w: canonical policy: %v", ErrInvalidOrchestratorInput, err)
	}
	if *input.Policy != canonical {
		return fmt.Errorf("%w: policy result does not match canonical decision", ErrInvalidOrchestratorInput)
	}
	return nil
}

type TurnDirective struct {
	Kind             NextAction
	Reason           ReasonCode
	Capability       string
	CapabilityStatus PolicyStatus
	Executable       bool
	Terminal         bool
	Handoff          bool
	ToolResult       *ToolResult
}

func (directive TurnDirective) Validate() error {
	decision, err := NewDecision(directive.Kind, directive.Reason)
	if err != nil {
		return fmt.Errorf("%w: decision: %v", ErrInvalidTurnDirective, err)
	}
	isCapability := decision.NextAction == ActionRequestCapability
	if isCapability {
		if strings.TrimSpace(directive.Capability) == "" {
			return fmt.Errorf("%w: capability is empty", ErrInvalidTurnDirective)
		}
		if err := ValidatePolicyStatus(directive.CapabilityStatus); err != nil {
			return fmt.Errorf("%w: capability status: %v", ErrInvalidTurnDirective, err)
		}
		if directive.Executable != (directive.CapabilityStatus == PolicyAllow) {
			return fmt.Errorf("%w: executable flag does not match policy status", ErrInvalidTurnDirective)
		}
		if directive.ToolResult != nil {
			if err := directive.ToolResult.Validate(); err != nil {
				return fmt.Errorf("%w: tool result: %v", ErrInvalidTurnDirective, err)
			}
			if directive.ToolResult.Tool != directive.Capability {
				return fmt.Errorf("%w: tool result does not match capability", ErrInvalidTurnDirective)
			}
			if directive.CapabilityStatus != PolicyAllow || !directive.Executable {
				return fmt.Errorf("%w: tool result requires an executable allowed capability", ErrInvalidTurnDirective)
			}
		}
	} else if directive.Capability != "" || directive.CapabilityStatus != "" || directive.Executable || directive.ToolResult != nil {
		return fmt.Errorf("%w: non-capability directive carries capability context", ErrInvalidTurnDirective)
	}

	switch directive.Kind {
	case ActionEndConversation:
		if !directive.Terminal || directive.Handoff {
			return fmt.Errorf("%w: end conversation must be terminal and not handoff", ErrInvalidTurnDirective)
		}
	case ActionHandoff:
		if directive.Terminal || !directive.Handoff {
			return fmt.Errorf("%w: handoff directive flags are invalid", ErrInvalidTurnDirective)
		}
	default:
		if directive.Terminal || directive.Handoff {
			return fmt.Errorf("%w: directive flags are invalid for action %q", ErrInvalidTurnDirective, directive.Kind)
		}
	}
	return nil
}

func BuildTurnDirective(input OrchestratorInput) (TurnDirective, error) {
	if err := input.Validate(); err != nil {
		return TurnDirective{}, err
	}

	directive := TurnDirective{
		Kind:   input.Decision.NextAction,
		Reason: input.Decision.Reason,
	}
	if input.Decision.NextAction == ActionRequestCapability {
		directive.Capability = input.RequestedTool
		directive.CapabilityStatus = input.Policy.Status
		directive.Executable = input.Policy.Status == PolicyAllow
		if input.ToolResult != nil {
			result := *input.ToolResult
			directive.ToolResult = &result
		}
	}
	switch input.Decision.NextAction {
	case ActionEndConversation:
		directive.Terminal = true
	case ActionHandoff:
		directive.Handoff = true
	}
	if err := directive.Validate(); err != nil {
		return TurnDirective{}, err
	}
	return directive, nil
}
