package salesintent

import (
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCanonicalIntentMapping(t *testing.T) {
	cases := []struct {
		class    Class
		action   conversation.NextAction
		reason   conversation.ReasonCode
		optedOut bool
	}{
		{Acceptance, conversation.ActionProposeScheduling, conversation.ReasonReadyToSchedule, false},
		{InternalAlignment, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
		{FutureFollowUp, conversation.ActionFollowUp, conversation.ReasonFollowUpRequired, false},
		{IndecisionCost, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
		{IndecisionSecurity, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
		{Rejection, conversation.ActionEndConversation, conversation.ReasonConversationComplete, false},
		{OptOut, conversation.ActionEndConversation, conversation.ReasonConversationComplete, true},
		{HumanRequest, conversation.ActionHandoff, conversation.ReasonHandoffRequired, false},
		{Clarification, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification, false},
		{CapabilityRequest, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification, false},
		{NeutralContinue, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.class), func(t *testing.T) {
			result, err := Decide(tc.class, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Class != tc.class || result.Decision.NextAction != tc.action || result.Decision.Reason != tc.reason || result.OptedOut != tc.optedOut {
				t.Fatalf("got %+v, want class=%s action=%s reason=%s optedOut=%v", result, tc.class, tc.action, tc.reason, tc.optedOut)
			}
		})
	}
}

func TestSanitizeLeadTextRedactsIdentifiersAndBounds(t *testing.T) {
	got := SanitizeLeadText("Meu email é joel@example.com e meu telefone é +55 67 98134-0687\n" + strings.Repeat("x", MaxLeadTextRunes+20))
	if strings.Contains(got, "joel@example.com") || strings.Contains(got, "98134-0687") {
		t.Fatalf("PII leaked: %q", got)
	}
	if utf8.RuneCountInString(got) > MaxLeadTextRunes {
		t.Fatalf("text has %d runes", utf8.RuneCountInString(got))
	}
}

func TestCapabilityRequiresAvailableContext(t *testing.T) {
	without, _ := Decide(CapabilityRequest, false)
	with, _ := Decide(CapabilityRequest, true)
	if without.Decision.NextAction == conversation.ActionRequestCapability {
		t.Fatal("requested unavailable capability")
	}
	if with.Decision.NextAction != conversation.ActionRequestCapability {
		t.Fatal("available capability not requested")
	}
}

func TestOptOutGuardIsExplicitAndNarrow(t *testing.T) {
	if !HasExplicitOptOut("Prefiro não continuar essa conversa. Por favor, retire meu contato.") {
		t.Fatal("explicit opt-out was not detected")
	}
	if HasExplicitOptOut("Não temos interesse neste momento") {
		t.Fatal("ordinary rejection was misclassified as opt-out")
	}
}
