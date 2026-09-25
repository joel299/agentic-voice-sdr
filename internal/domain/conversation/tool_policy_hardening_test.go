package conversation

import (
	"errors"
	"testing"

	domainTools "github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

func forgedPolicyInput(t *testing.T, tool string, effect ToolEffect) ToolPolicyInput {
	t.Helper()
	state := activePolicyState(t)
	decisionInput, err := NewDecisionInput(state)
	if err != nil {
		t.Fatalf("create decision input: %v", err)
	}
	return ToolPolicyInput{
		DecisionInput: decisionInput,
		Decision:      capabilityDecision(t),
		RequestedTool: tool,
		Effect:        effect,
	}
}

func optedOutPolicyState(t *testing.T) *ConversationState {
	t.Helper()
	state := activePolicyState(t)
	if err := state.RecordSignal(SignalOptedOut); err != nil {
		t.Fatalf("record opt-out: %v", err)
	}
	return state
}

func TestToolPolicyRejectsForgedSideEffectAsRead(t *testing.T) {
	input := forgedPolicyInput(t, domainTools.ToolCalendarCreateEvent, ToolEffectRead)
	if _, err := EvaluateToolPolicy(input); !errors.Is(err, ErrInvalidToolPolicyInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidToolPolicyInput)
	}
}

func TestToolPolicyRejectsForgedUnknownAsRead(t *testing.T) {
	input := forgedPolicyInput(t, "unknown.tool", ToolEffectRead)
	if _, err := EvaluateToolPolicy(input); !errors.Is(err, ErrInvalidToolPolicyInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidToolPolicyInput)
	}
}

func TestCanonicalToolEffectsAreDerivedFromToolID(t *testing.T) {
	tests := []struct {
		tool   string
		effect ToolEffect
	}{
		{tool: domainTools.ToolMemorySearch, effect: ToolEffectRead},
		{tool: domainTools.ToolCalendarCreateEvent, effect: ToolEffectSideEffect},
		{tool: "unknown.tool", effect: ToolEffectUnknown},
	}
	for _, test := range tests {
		input := forgedPolicyInput(t, test.tool, test.effect)
		if err := input.Validate(); err != nil {
			t.Fatalf("tool %q: validate: %v", test.tool, err)
		}
	}
}

func TestOptOutDeniesWhatsAppContact(t *testing.T) {
	input, err := NewToolPolicyInput(optedOutPolicyState(t), capabilityDecision(t), domainTools.ToolWhatsAppSendMessage)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonContactOptedOut}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}

func TestOptOutDeniesCallbackContact(t *testing.T) {
	input, err := NewToolPolicyInput(optedOutPolicyState(t), capabilityDecision(t), domainTools.ToolCallbackSchedule)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonContactOptedOut}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}

func TestOptOutDoesNotBlockInternalNote(t *testing.T) {
	input, err := NewToolPolicyInput(optedOutPolicyState(t), capabilityDecision(t), domainTools.ToolConversationAddNote)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}

func TestContactToolRemainsAllowedWithoutOptOut(t *testing.T) {
	input, err := NewToolPolicyInput(activePolicyState(t), capabilityDecision(t), domainTools.ToolWhatsAppSendMessage)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}
