package conversation

import (
	"errors"
	"testing"

	domainTools "github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

func activePolicyState(t *testing.T) *ConversationState {
	t.Helper()
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	if _, err := state.Transition(StageActive); err != nil {
		t.Fatalf("activate state: %v", err)
	}
	return state
}

func capabilityDecision(t *testing.T) Decision {
	t.Helper()
	decision, err := NewDecision(ActionRequestCapability, ReasonCapabilityRequired)
	if err != nil {
		t.Fatalf("create capability decision: %v", err)
	}
	return decision
}

func TestToolPolicyAllowsKnownReadToolForCapabilityDecision(t *testing.T) {
	input, err := NewToolPolicyInput(activePolicyState(t), capabilityDecision(t), domainTools.ToolMemorySearch)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	if input.Effect != ToolEffectRead {
		t.Fatalf("effect = %q, want %q", input.Effect, ToolEffectRead)
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

func TestToolPolicyDeniesUnknownTool(t *testing.T) {
	input, err := NewToolPolicyInput(activePolicyState(t), capabilityDecision(t), "calendar.unknown")
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	if result.Status != PolicyDeny || result.Reason != PolicyReasonUnknownTool {
		t.Fatalf("result = %+v, want deny/unknown_tool", result)
	}
}

func TestToolPolicyDefersSideEffectUntilConversationIsReady(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	input, err := NewToolPolicyInput(state, capabilityDecision(t), domainTools.ToolLeadUpdate)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	if input.Effect != ToolEffectSideEffect {
		t.Fatalf("effect = %q, want %q", input.Effect, ToolEffectSideEffect)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyDefer, Reason: PolicyReasonMissingRequiredState}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}

func TestToolPolicyDeniesSideEffectForIncompatibleDecision(t *testing.T) {
	state := activePolicyState(t)
	decision, err := NewDecision(ActionContinueConversation, ReasonContinueDiscovery)
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	input, err := NewToolPolicyInput(state, decision, domainTools.ToolMemoryStore)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	result, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	want := ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonSideEffectNotAuthorized}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}

func TestToolPolicyRejectsInvalidInputDecision(t *testing.T) {
	state := activePolicyState(t)
	invalid := Decision{NextAction: NextAction("unknown"), Reason: ReasonCode("unknown")}
	if _, err := NewToolPolicyInput(state, invalid, domainTools.ToolMemorySearch); !errors.Is(err, ErrInvalidToolPolicyInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidToolPolicyInput)
	}
	if _, err := NewToolPolicyInput(state, capabilityDecision(t), " "); !errors.Is(err, ErrInvalidToolPolicyInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidToolPolicyInput)
	}
}

func TestPolicyResultsHaveCanonicalStatuses(t *testing.T) {
	for _, status := range []PolicyStatus{PolicyAllow, PolicyDeny, PolicyDefer} {
		if err := ValidatePolicyStatus(status); err != nil {
			t.Errorf("status %q: %v", status, err)
		}
	}
	if err := ValidatePolicyStatus(PolicyStatus("unknown")); !errors.Is(err, ErrInvalidPolicyStatus) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidPolicyStatus)
	}
}

func TestToolPolicyIsDeterministic(t *testing.T) {
	input, err := NewToolPolicyInput(activePolicyState(t), capabilityDecision(t), domainTools.ToolMemorySearch)
	if err != nil {
		t.Fatalf("create policy input: %v", err)
	}
	first, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("first evaluation: %v", err)
	}
	second, err := EvaluateToolPolicy(input)
	if err != nil {
		t.Fatalf("second evaluation: %v", err)
	}
	if first != second {
		t.Fatalf("results differ: first=%+v second=%+v", first, second)
	}
}

func TestAllCanonicalToolsAreRecognized(t *testing.T) {
	for _, name := range domainTools.InitialToolNames() {
		input, err := NewToolPolicyInput(activePolicyState(t), capabilityDecision(t), name)
		if err != nil {
			t.Fatalf("tool %q: create policy input: %v", name, err)
		}
		if input.Effect == ToolEffectUnknown {
			t.Fatalf("tool %q was not classified", name)
		}
	}
}
