package conversation

import (
	"errors"
	"strings"
	"testing"
)

func TestDecisionInputFromStateUsesMinimalSnapshot(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	if _, err := state.Transition(StageActive); err != nil {
		t.Fatalf("activate conversation: %v", err)
	}
	turn, err := NewTurn("turn-1", RoleLead, "tenho interesse", TranscriptFinal)
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := state.RecordTurn(turn); err != nil {
		t.Fatalf("record turn: %v", err)
	}

	input, err := NewDecisionInput(state)
	if err != nil {
		t.Fatalf("create decision input: %v", err)
	}
	want := DecisionInput{
		Stage:               StageActive,
		Signals:             Signals{LeadResponded: true},
		TurnCount:           1,
		LastTurnRole:        RoleLead,
		LastTranscriptState: TranscriptFinal,
		LatestFinalLeadText: "tenho interesse",
	}
	if input != want {
		t.Fatalf("input = %+v, want %+v", input, want)
	}
}

func TestDecisionInputRejectsNilState(t *testing.T) {
	if _, err := NewDecisionInput(nil); !errors.Is(err, ErrInvalidDecisionInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidDecisionInput)
	}
}

func TestDecisionInputBoundsAndScopesLatestFinalLeadText(t *testing.T) {
	valid := DecisionInput{Stage: StageActive, TurnCount: 1, LastTurnRole: RoleLead, LastTranscriptState: TranscriptFinal, LatestFinalLeadText: "lead answer"}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid final lead input: %v", err)
	}
	invalid := valid
	invalid.LastTurnRole = RoleAgent
	if err := invalid.validate(); !errors.Is(err, ErrInvalidDecisionInput) {
		t.Fatalf("agent transcript error=%v", err)
	}
	invalid = valid
	invalid.LatestFinalLeadText = strings.Repeat("x", maxFinalLeadTextRunes+1)
	if err := invalid.validate(); !errors.Is(err, ErrInvalidDecisionInput) {
		t.Fatalf("unbounded transcript error=%v", err)
	}
}

func TestCanonicalNextActionsAreValid(t *testing.T) {
	for _, action := range []NextAction{
		ActionContinueConversation,
		ActionAskQuestion,
		ActionProposeScheduling,
		ActionRequestCapability,
		ActionFollowUp,
		ActionEndConversation,
		ActionHandoff,
	} {
		if err := ValidateNextAction(action); err != nil {
			t.Errorf("action %q: %v", action, err)
		}
	}
	if err := ValidateNextAction(NextAction("unknown")); !errors.Is(err, ErrInvalidNextAction) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidNextAction)
	}
}

func TestDecisionPreservesActionAndReason(t *testing.T) {
	decision, err := NewDecision(ActionAskQuestion, ReasonNeedsClarification)
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	if decision.NextAction != ActionAskQuestion || decision.Reason != ReasonNeedsClarification {
		t.Fatalf("decision = %+v, want action and reason preserved", decision)
	}
}

func TestDecisionRejectsIncompatibleReason(t *testing.T) {
	if _, err := NewDecision(ActionProposeScheduling, ReasonConversationComplete); !errors.Is(err, ErrIncompatibleDecision) {
		t.Fatalf("error = %v, want %v", err, ErrIncompatibleDecision)
	}
	if _, err := NewDecision(NextAction("unknown"), ReasonContinueDiscovery); !errors.Is(err, ErrInvalidNextAction) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidNextAction)
	}
}

func TestDecisionContractsAreDeterministic(t *testing.T) {
	first, err := NewDecision(ActionContinueConversation, ReasonContinueDiscovery)
	if err != nil {
		t.Fatalf("create first decision: %v", err)
	}
	second, err := NewDecision(ActionContinueConversation, ReasonContinueDiscovery)
	if err != nil {
		t.Fatalf("create second decision: %v", err)
	}
	if first != second {
		t.Fatalf("decisions differ: first=%+v second=%+v", first, second)
	}
}
