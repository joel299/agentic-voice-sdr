// Package salesintent classifies one bounded, final lead turn and maps the
// typed result to canonical conversation decisions. It never generates copy.
package salesintent

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
)

const MaxLeadTextRunes = 1200

type Class string

const (
	Acceptance         Class = "acceptance"
	IndecisionCost     Class = "indecision_cost"
	IndecisionSecurity Class = "indecision_security"
	IndecisionTiming   Class = "indecision_timing_or_internal_alignment"
	Rejection          Class = "rejection"
	OptOut             Class = "opt_out"
	HumanRequest       Class = "human_request"
	Clarification      Class = "clarification_or_information_request"
	CapabilityRequest  Class = "capability_request"
	NeutralContinue    Class = "neutral_continue"
)

type Result struct {
	Class    Class
	Decision conversation.Decision
	OptedOut bool
}

var (
	emailRE = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	phoneRE = regexp.MustCompile(`(?:\+?\d[\d .()\-]{7,}\d)`)
)

// SanitizeLeadText bounds one final lead turn and removes common direct
// identifiers and control characters before it can cross the JEV boundary.
func SanitizeLeadText(text string) string {
	text = strings.TrimSpace(text)
	text = emailRE.ReplaceAllString(text, "[email]")
	text = phoneRE.ReplaceAllString(text, "[phone]")
	var b strings.Builder
	n := 0
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		if n == MaxLeadTextRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func normalized(text string) string {
	text = strings.ToLower(SanitizeLeadText(text))
	text = strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "ô", "o", "õ", "o", "ú", "u", "ç", "c").Replace(text)
	text = strings.NewReplacer("?", " ", ".", " ", ",", " ", "!", " ", ";", " ").Replace(text)
	return " " + strings.Join(strings.Fields(text), " ") + " "
}

func hasAny(s string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(s, " "+phrase+" ") {
			return true
		}
	}
	return false
}

// HasExplicitOptOut is the narrow, phrase-based safety guard. All ordinary
// commercial intent is classified semantically by JEV.
func HasExplicitOptOut(text string) bool {
	s := normalized(text)
	return hasAny(s, "pare", "parar", "retire meu contato", "nao me ligue", "nao ligar", "remova meu contato", "descadastrar", "opt out", "nao entre mais em contato", "nao quero receber contato")
}

// Decide deterministically maps a class to the existing decision taxonomy.
// Future contact uses follow_up only when the text contains an explicit date
// or period; otherwise timing objections remain in discovery.
func Decide(class Class, explicitFutureContact bool, capabilityAvailable bool) (Result, error) {
	var action conversation.NextAction
	var reason conversation.ReasonCode
	optedOut := false
	switch class {
	case Acceptance:
		action, reason = conversation.ActionProposeScheduling, conversation.ReasonReadyToSchedule
	case IndecisionTiming:
		if explicitFutureContact {
			action, reason = conversation.ActionFollowUp, conversation.ReasonFollowUpRequired
		} else {
			action, reason = conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery
		}
	case Rejection:
		action, reason = conversation.ActionEndConversation, conversation.ReasonConversationComplete
	case OptOut:
		action, reason, optedOut = conversation.ActionEndConversation, conversation.ReasonConversationComplete, true
	case HumanRequest:
		action, reason = conversation.ActionHandoff, conversation.ReasonHandoffRequired
	case Clarification:
		action, reason = conversation.ActionAskQuestion, conversation.ReasonNeedsClarification
	case CapabilityRequest:
		if capabilityAvailable {
			action, reason = conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired
		} else {
			action, reason = conversation.ActionAskQuestion, conversation.ReasonNeedsClarification
		}
	case IndecisionCost, IndecisionSecurity, NeutralContinue:
		action, reason = conversation.ActionContinueConversation, conversation.ReasonContinueDiscovery
	default:
		return Result{}, conversation.ErrInvalidDecisionInput
	}
	d, err := conversation.NewDecision(action, reason)
	if err != nil {
		return Result{}, err
	}
	return Result{Class: class, Decision: d, OptedOut: optedOut}, nil
}
