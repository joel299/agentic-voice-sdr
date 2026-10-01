package salesintent

import (
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClassifierFixtures(t *testing.T) {
	cases := []struct {
		text     string
		class    Class
		action   conversation.NextAction
		reason   conversation.ReasonCode
		optedOut bool
	}{
		{"Quinta às 10h funciona", Acceptance, conversation.ActionProposeScheduling, conversation.ReasonReadyToSchedule, false},
		{"Quero ver isso. Podemos marcar?", Acceptance, conversation.ActionProposeScheduling, conversation.ReasonReadyToSchedule, false},
		{"Preciso alinhar com a diretoria e te retorno segunda", IndecisionTiming, conversation.ActionFollowUp, conversation.ReasonFollowUpRequired, false},
		{"Estamos com budget congelado", IndecisionCost, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
		{"Tenho receio de LGPD e alucinação", IndecisionSecurity, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
		{"Não temos interesse", Rejection, conversation.ActionEndConversation, conversation.ReasonConversationComplete, false},
		{"Parar", OptOut, conversation.ActionEndConversation, conversation.ReasonConversationComplete, true},
		{"Retire meu contato", OptOut, conversation.ActionEndConversation, conversation.ReasonConversationComplete, true},
		{"Quero falar com uma pessoa", HumanRequest, conversation.ActionHandoff, conversation.ReasonHandoffRequired, false},
		{"Quais informações vocês precisam?", Clarification, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification, false},
		{"Vocês integram com meu CRM?", CapabilityRequest, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification, false},
		{"Entendi, obrigado", NeutralContinue, conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery, false},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			result, err := ClassifyAndDecide(tc.text, tc.class == IndecisionTiming, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Class != tc.class || result.Decision.NextAction != tc.action || result.Decision.Reason != tc.reason || result.OptedOut != tc.optedOut {
				t.Fatalf("got %+v, want class=%s action=%s reason=%s optedOut=%v", result, tc.class, tc.action, tc.reason, tc.optedOut)
			}
		})
	}
}

func TestClassifierDoesNotPromoteAmbiguousInterestToScheduling(t *testing.T) {
	result, err := ClassifyAndDecide("Tenho interesse em saber mais", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != NeutralContinue || result.Decision.NextAction == conversation.ActionProposeScheduling {
		t.Fatalf("ambiguous interest promoted: %+v", result)
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
	without, _ := Decide(CapabilityRequest, false, false)
	with, _ := Decide(CapabilityRequest, false, true)
	if without.Decision.NextAction == conversation.ActionRequestCapability {
		t.Fatal("requested unavailable capability")
	}
	if with.Decision.NextAction != conversation.ActionRequestCapability {
		t.Fatal("available capability not requested")
	}
}

func TestFutureContactRequiresExplicitPeriod(t *testing.T) {
	if !HasExplicitFutureContact("te retorno segunda") {
		t.Fatal("explicit follow-up time was not detected")
	}
	if HasExplicitFutureContact("preciso alinhar internamente") {
		t.Fatal("unspecified timing was treated as a follow-up commitment")
	}
}
