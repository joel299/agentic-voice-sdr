package conversation

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidDecisionInput = errors.New("invalid sales decision input")
	ErrInvalidNextAction    = errors.New("invalid next action")
	ErrInvalidReasonCode    = errors.New("invalid decision reason code")
	ErrIncompatibleDecision = errors.New("decision reason is incompatible with next action")
)

type DecisionInput struct {
	Stage               ConversationStage
	Signals             Signals
	TurnCount           int
	LastTurnRole        ParticipantRole
	LastTranscriptState TranscriptState
}

func NewDecisionInput(state *ConversationState) (DecisionInput, error) {
	if state == nil || !validDecisionStage(state.Stage()) {
		return DecisionInput{}, ErrInvalidDecisionInput
	}
	turns := state.Turns()
	input := DecisionInput{
		Stage:     state.Stage(),
		Signals:   state.Signals(),
		TurnCount: len(turns),
	}
	if len(turns) > 0 {
		last := turns[len(turns)-1]
		input.LastTurnRole = last.Role
		input.LastTranscriptState = last.Transcript
	}
	return input, nil
}

type NextAction string

const (
	ActionContinueConversation NextAction = "continue_conversation"
	ActionAskQuestion          NextAction = "ask_question"
	ActionProposeScheduling    NextAction = "propose_scheduling"
	ActionRequestCapability    NextAction = "request_capability"
	ActionFollowUp             NextAction = "follow_up"
	ActionEndConversation      NextAction = "end_conversation"
	ActionHandoff              NextAction = "handoff"
)

func ValidateNextAction(action NextAction) error {
	if _, ok := reasonForAction(action); !ok {
		return fmt.Errorf("%w: %q", ErrInvalidNextAction, action)
	}
	return nil
}

type ReasonCode string

const (
	ReasonNeedsClarification   ReasonCode = "needs_clarification"
	ReasonContinueDiscovery    ReasonCode = "continue_discovery"
	ReasonInterestConfirmed    ReasonCode = "interest_confirmed"
	ReasonReadyToSchedule      ReasonCode = "ready_to_schedule"
	ReasonConversationComplete ReasonCode = "conversation_complete"
	ReasonFollowUpRequired     ReasonCode = "follow_up_required"
	ReasonCapabilityRequired   ReasonCode = "capability_required"
	ReasonHandoffRequired      ReasonCode = "handoff_required"
)

type Decision struct {
	NextAction NextAction
	Reason     ReasonCode
}

func NewDecision(action NextAction, reason ReasonCode) (Decision, error) {
	decision := Decision{NextAction: action, Reason: reason}
	if err := decision.Validate(); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func (d Decision) Validate() error {
	if err := ValidateNextAction(d.NextAction); err != nil {
		return err
	}
	if !validReasonCode(d.Reason) {
		return fmt.Errorf("%w: %q", ErrInvalidReasonCode, d.Reason)
	}
	if !reasonCompatibleWithAction(d.NextAction, d.Reason) {
		return fmt.Errorf("%w: action %q does not accept reason %q", ErrIncompatibleDecision, d.NextAction, d.Reason)
	}
	return nil
}

func reasonCompatibleWithAction(action NextAction, reason ReasonCode) bool {
	if action == ActionProposeScheduling && reason == ReasonInterestConfirmed {
		return true
	}
	expected, ok := reasonForAction(action)
	return ok && expected == reason
}

func reasonForAction(action NextAction) (ReasonCode, bool) {
	switch action {
	case ActionContinueConversation:
		return ReasonContinueDiscovery, true
	case ActionAskQuestion:
		return ReasonNeedsClarification, true
	case ActionProposeScheduling:
		return ReasonReadyToSchedule, true
	case ActionRequestCapability:
		return ReasonCapabilityRequired, true
	case ActionFollowUp:
		return ReasonFollowUpRequired, true
	case ActionEndConversation:
		return ReasonConversationComplete, true
	case ActionHandoff:
		return ReasonHandoffRequired, true
	default:
		return "", false
	}
}

func validReasonCode(reason ReasonCode) bool {
	switch reason {
	case ReasonNeedsClarification,
		ReasonContinueDiscovery,
		ReasonInterestConfirmed,
		ReasonReadyToSchedule,
		ReasonConversationComplete,
		ReasonFollowUpRequired,
		ReasonCapabilityRequired,
		ReasonHandoffRequired:
		return true
	default:
		return false
	}
}

func validDecisionStage(stage ConversationStage) bool {
	switch stage {
	case StageOpening, StageActive, StageClosing, StageEnded:
		return true
	default:
		return false
	}
}
