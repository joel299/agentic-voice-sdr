package conversation

import (
	"errors"
	"fmt"
	"strings"

	domainTools "github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

var (
	ErrInvalidToolPolicyInput = errors.New("invalid tool policy input")
	ErrInvalidPolicyStatus    = errors.New("invalid tool policy status")
	ErrInvalidPolicyReason    = errors.New("invalid tool policy reason")
)

type ToolEffect string

const (
	ToolEffectUnknown    ToolEffect = "unknown"
	ToolEffectRead       ToolEffect = "read"
	ToolEffectSideEffect ToolEffect = "side_effect"
)

type ToolPolicyInput struct {
	DecisionInput DecisionInput
	Decision      Decision
	RequestedTool string
	Effect        ToolEffect
}

func NewToolPolicyInput(state *ConversationState, decision Decision, requestedTool string) (ToolPolicyInput, error) {
	if strings.TrimSpace(requestedTool) == "" {
		return ToolPolicyInput{}, ErrInvalidToolPolicyInput
	}
	if err := decision.Validate(); err != nil {
		return ToolPolicyInput{}, fmt.Errorf("%w: decision: %v", ErrInvalidToolPolicyInput, err)
	}
	input, err := NewDecisionInput(state)
	if err != nil {
		return ToolPolicyInput{}, fmt.Errorf("%w: conversation state: %v", ErrInvalidToolPolicyInput, err)
	}
	return ToolPolicyInput{
		DecisionInput: input,
		Decision:      decision,
		RequestedTool: requestedTool,
		Effect:        classifyToolEffect(requestedTool),
	}, nil
}

func (input ToolPolicyInput) Validate() error {
	if err := input.DecisionInput.validate(); err != nil {
		return fmt.Errorf("%w: decision input: %v", ErrInvalidToolPolicyInput, err)
	}
	if err := input.Decision.Validate(); err != nil {
		return fmt.Errorf("%w: decision: %v", ErrInvalidToolPolicyInput, err)
	}
	if strings.TrimSpace(input.RequestedTool) == "" {
		return fmt.Errorf("%w: requested tool is empty", ErrInvalidToolPolicyInput)
	}
	expectedEffect := classifyToolEffect(input.RequestedTool)
	if input.Effect != expectedEffect {
		return fmt.Errorf("%w: effect %q does not match tool %q classification %q", ErrInvalidToolPolicyInput, input.Effect, input.RequestedTool, expectedEffect)
	}
	switch input.Effect {
	case ToolEffectUnknown, ToolEffectRead, ToolEffectSideEffect:
		return nil
	default:
		return fmt.Errorf("%w: unknown tool effect %q", ErrInvalidToolPolicyInput, input.Effect)
	}
}

func (input DecisionInput) validate() error {
	if !validDecisionStage(input.Stage) || input.TurnCount < 0 {
		return ErrInvalidDecisionInput
	}
	if input.TurnCount == 0 {
		if input.LastTurnRole != "" || input.LastTranscriptState != "" {
			return ErrInvalidDecisionInput
		}
		return nil
	}
	if !validRole(input.LastTurnRole) || !validTranscriptState(input.LastTranscriptState) {
		return ErrInvalidDecisionInput
	}
	return nil
}

type PolicyStatus string

const (
	PolicyAllow PolicyStatus = "allow"
	PolicyDeny  PolicyStatus = "deny"
	PolicyDefer PolicyStatus = "defer"
)

func ValidatePolicyStatus(status PolicyStatus) error {
	switch status {
	case PolicyAllow, PolicyDeny, PolicyDefer:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPolicyStatus, status)
	}
}

type PolicyReason string

const (
	PolicyReasonAllowedByDecision       PolicyReason = "allowed_by_decision"
	PolicyReasonUnknownTool             PolicyReason = "unknown_tool"
	PolicyReasonIncompatibleAction      PolicyReason = "incompatible_action"
	PolicyReasonMissingRequiredState    PolicyReason = "missing_required_state"
	PolicyReasonSideEffectNotAuthorized PolicyReason = "side_effect_not_authorized"
	PolicyReasonDeferUntilReady         PolicyReason = "defer_until_ready"
	PolicyReasonContactOptedOut         PolicyReason = "contact_opted_out"
)

func validatePolicyReason(reason PolicyReason) error {
	switch reason {
	case PolicyReasonAllowedByDecision,
		PolicyReasonUnknownTool,
		PolicyReasonIncompatibleAction,
		PolicyReasonMissingRequiredState,
		PolicyReasonSideEffectNotAuthorized,
		PolicyReasonDeferUntilReady,
		PolicyReasonContactOptedOut:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPolicyReason, reason)
	}
}

type ToolPolicyResult struct {
	Status PolicyStatus
	Reason PolicyReason
}

func (result ToolPolicyResult) Validate() error {
	if err := ValidatePolicyStatus(result.Status); err != nil {
		return err
	}
	return validatePolicyReason(result.Reason)
}

func EvaluateToolPolicy(input ToolPolicyInput) (ToolPolicyResult, error) {
	if err := input.Validate(); err != nil {
		return ToolPolicyResult{}, err
	}
	if input.Effect == ToolEffectUnknown {
		return ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonUnknownTool}, nil
	}
	if input.DecisionInput.Signals.OptedOut && isContactTool(input.RequestedTool) {
		return ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonContactOptedOut}, nil
	}
	if input.DecisionInput.Stage == StageEnded {
		return ToolPolicyResult{Status: PolicyDefer, Reason: PolicyReasonMissingRequiredState}, nil
	}
	if input.Decision.NextAction == ActionRequestCapability {
		if input.Effect == ToolEffectSideEffect && input.DecisionInput.Stage == StageOpening {
			return ToolPolicyResult{Status: PolicyDefer, Reason: PolicyReasonMissingRequiredState}, nil
		}
		return ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}, nil
	}
	if input.Effect == ToolEffectSideEffect {
		return ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonSideEffectNotAuthorized}, nil
	}
	if input.Decision.NextAction == ActionProposeScheduling &&
		input.RequestedTool == domainTools.ToolCalendarCheckAvailability {
		return ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}, nil
	}
	return ToolPolicyResult{Status: PolicyDefer, Reason: PolicyReasonDeferUntilReady}, nil
}

func classifyToolEffect(name string) ToolEffect {
	switch name {
	case domainTools.ToolCalendarCheckAvailability, domainTools.ToolMemorySearch:
		return ToolEffectRead
	case domainTools.ToolCalendarCreateEvent,
		domainTools.ToolWhatsAppSendMessage,
		domainTools.ToolMemoryStore,
		domainTools.ToolLeadUpdate,
		domainTools.ToolConversationAddNote,
		domainTools.ToolCallbackSchedule:
		return ToolEffectSideEffect
	default:
		return ToolEffectUnknown
	}
}

func isContactTool(name string) bool {
	switch name {
	case domainTools.ToolWhatsAppSendMessage, domainTools.ToolCallbackSchedule:
		return true
	default:
		return false
	}
}
