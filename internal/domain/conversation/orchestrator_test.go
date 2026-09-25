package conversation

import (
	"errors"
	"testing"
)

func validOrchestratorInput(action NextAction, reason ReasonCode) OrchestratorInput {
	decision, err := NewDecision(action, reason)
	if err != nil {
		panic(err)
	}
	return OrchestratorInput{
		DecisionInput: DecisionInput{
			Stage:               StageActive,
			TurnCount:           1,
			LastTurnRole:        RoleLead,
			LastTranscriptState: TranscriptFinal,
		},
		Decision: decision,
	}
}

func TestOrchestratorRejectsInvalidDecision(t *testing.T) {
	input := validOrchestratorInput(ActionAskQuestion, ReasonNeedsClarification)
	input.Decision = Decision{NextAction: NextAction("unknown"), Reason: ReasonNeedsClarification}

	if _, err := BuildTurnDirective(input); !errors.Is(err, ErrInvalidOrchestratorInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidOrchestratorInput)
	}
}

func TestOrchestratorRejectsInvalidPolicyResult(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.RequestedTool = "calendar.check_availability"
	input.Policy = &ToolPolicyResult{Status: PolicyStatus("unknown"), Reason: PolicyReasonAllowedByDecision}

	if _, err := BuildTurnDirective(input); !errors.Is(err, ErrInvalidOrchestratorInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidOrchestratorInput)
	}
}

func TestOrchestratorRequiresPolicyForCapabilityRequest(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.RequestedTool = "calendar.check_availability"

	if _, err := BuildTurnDirective(input); !errors.Is(err, ErrInvalidOrchestratorInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidOrchestratorInput)
	}
}

func TestDeniedCapabilityCannotBecomeExecutableDirective(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.DecisionInput.Signals.OptedOut = true
	input.RequestedTool = "whatsapp.send_message"
	input.Policy = &ToolPolicyResult{Status: PolicyDeny, Reason: PolicyReasonContactOptedOut}

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if directive.Executable || directive.CapabilityStatus != PolicyDeny {
		t.Fatalf("directive = %+v, denied capability must not be executable", directive)
	}
}

func TestDeferredCapabilityCannotBecomeExecutableDirective(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.DecisionInput.Stage = StageOpening
	input.RequestedTool = "calendar.create_event"
	input.Policy = &ToolPolicyResult{Status: PolicyDefer, Reason: PolicyReasonMissingRequiredState}

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if directive.Executable || directive.CapabilityStatus != PolicyDefer {
		t.Fatalf("directive = %+v, deferred capability must not be executable", directive)
	}
}

func TestAllowedCapabilityIsRepresentedWithoutExecution(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.RequestedTool = "calendar.check_availability"
	input.Policy = &ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if !directive.Executable || directive.Capability != input.RequestedTool || directive.CapabilityStatus != PolicyAllow {
		t.Fatalf("directive = %+v, want allowed capability representation", directive)
	}
}

func TestOrchestratorPreservesToolResultWithoutExecutingIt(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.RequestedTool = "calendar.check_availability"
	input.Policy = &ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}
	input.ToolResult = &ToolResult{Tool: input.RequestedTool, Status: ToolResultSucceeded}

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if directive.ToolResult == nil || *directive.ToolResult != *input.ToolResult {
		t.Fatalf("directive tool result = %+v, want %+v", directive.ToolResult, input.ToolResult)
	}
}

func TestEndConversationProducesTerminalDirective(t *testing.T) {
	input := validOrchestratorInput(ActionEndConversation, ReasonConversationComplete)

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if !directive.Terminal || directive.Handoff || directive.Kind != ActionEndConversation {
		t.Fatalf("directive = %+v, want terminal end directive", directive)
	}
}

func TestHandoffProducesHandoffDirective(t *testing.T) {
	input := validOrchestratorInput(ActionHandoff, ReasonHandoffRequired)

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if !directive.Handoff || directive.Terminal || directive.Kind != ActionHandoff {
		t.Fatalf("directive = %+v, want handoff directive", directive)
	}
}

func TestNonCapabilityDecisionDoesNotRequirePolicy(t *testing.T) {
	input := validOrchestratorInput(ActionAskQuestion, ReasonNeedsClarification)

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if directive.Capability != "" || directive.Executable {
		t.Fatalf("directive = %+v, want non-capability directive", directive)
	}
}

func TestOrchestratorDirectivesAreDeterministic(t *testing.T) {
	input := validOrchestratorInput(ActionProposeScheduling, ReasonReadyToSchedule)

	first, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build first directive: %v", err)
	}
	second, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build second directive: %v", err)
	}
	if first != second {
		t.Fatalf("directives differ: first=%+v second=%+v", first, second)
	}
}

func TestToolResultValidationRejectsMissingTool(t *testing.T) {
	result := ToolResult{Status: ToolResultSucceeded}
	if !errors.Is(result.Validate(), ErrInvalidToolResult) {
		t.Fatalf("error = %v, want %v", result.Validate(), ErrInvalidToolResult)
	}
}

func TestOrchestratorRejectsPolicyReplayedForDifferentTool(t *testing.T) {
	decision, err := NewDecision(ActionRequestCapability, ReasonCapabilityRequired)
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	decisionInput := DecisionInput{
		Stage:               StageActive,
		Signals:             Signals{OptedOut: true},
		TurnCount:           1,
		LastTurnRole:        RoleLead,
		LastTranscriptState: TranscriptFinal,
	}
	original, err := EvaluateToolPolicy(ToolPolicyInput{
		DecisionInput: decisionInput,
		Decision:      decision,
		RequestedTool: "conversation.add_note",
		Effect:        ToolEffectSideEffect,
	})
	if err != nil {
		t.Fatalf("evaluate original policy: %v", err)
	}
	if original.Status != PolicyAllow {
		t.Fatalf("original policy = %+v, want allow", original)
	}

	_, err = BuildTurnDirective(OrchestratorInput{
		DecisionInput: decisionInput,
		Decision:      decision,
		RequestedTool: "whatsapp.send_message",
		Policy:        &original,
	})
	if !errors.Is(err, ErrInvalidOrchestratorInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidOrchestratorInput)
	}
}

func TestOrchestratorRejectsForgedAllowForOptedOutContact(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.DecisionInput.Signals.OptedOut = true
	input.RequestedTool = "whatsapp.send_message"
	input.Policy = &ToolPolicyResult{Status: PolicyAllow, Reason: PolicyReasonAllowedByDecision}

	if _, err := BuildTurnDirective(input); !errors.Is(err, ErrInvalidOrchestratorInput) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidOrchestratorInput)
	}
}

func TestOrchestratorAcceptsMatchingCanonicalPolicy(t *testing.T) {
	input := validOrchestratorInput(ActionRequestCapability, ReasonCapabilityRequired)
	input.RequestedTool = "calendar.check_availability"
	policy, err := EvaluateToolPolicy(ToolPolicyInput{
		DecisionInput: input.DecisionInput,
		Decision:      input.Decision,
		RequestedTool: input.RequestedTool,
		Effect:        ToolEffectRead,
	})
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	input.Policy = &policy

	directive, err := BuildTurnDirective(input)
	if err != nil {
		t.Fatalf("build directive: %v", err)
	}
	if !directive.Executable {
		t.Fatalf("directive = %+v, want executable allowed capability", directive)
	}
}

func TestTurnDirectiveValidatesNestedToolResult(t *testing.T) {
	tests := []struct {
		name       string
		status     PolicyStatus
		executable bool
		result     *ToolResult
		wantError  bool
	}{
		{
			name:       "empty tool",
			status:     PolicyAllow,
			executable: true,
			result:     &ToolResult{Status: ToolResultSucceeded},
			wantError:  true,
		},
		{
			name:       "invalid result status",
			status:     PolicyAllow,
			executable: true,
			result:     &ToolResult{Tool: "calendar.check_availability", Status: ToolResultStatus("unknown")},
			wantError:  true,
		},
		{
			name:       "mismatched tool",
			status:     PolicyAllow,
			executable: true,
			result:     &ToolResult{Tool: "calendar.create_event", Status: ToolResultSucceeded},
			wantError:  true,
		},
		{
			name:       "denied with result",
			status:     PolicyDeny,
			executable: false,
			result:     &ToolResult{Tool: "calendar.check_availability", Status: ToolResultSucceeded},
			wantError:  true,
		},
		{
			name:       "deferred with result",
			status:     PolicyDefer,
			executable: false,
			result:     &ToolResult{Tool: "calendar.check_availability", Status: ToolResultSucceeded},
			wantError:  true,
		},
		{
			name:       "allowed matching result",
			status:     PolicyAllow,
			executable: true,
			result:     &ToolResult{Tool: "calendar.check_availability", Status: ToolResultSucceeded},
			wantError:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directive := TurnDirective{
				Kind:             ActionRequestCapability,
				Reason:           ReasonCapabilityRequired,
				Capability:       "calendar.check_availability",
				CapabilityStatus: test.status,
				Executable:       test.executable,
				ToolResult:       test.result,
			}
			err := directive.Validate()
			if test.wantError && !errors.Is(err, ErrInvalidTurnDirective) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidTurnDirective)
			}
			if !test.wantError && err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}
