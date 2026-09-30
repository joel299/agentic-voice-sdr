package geminilive

import (
	"context"
	"fmt"
	"sync"
)

// LeadTurnSequencer owns monotonically increasing IDs for one application call.
// Keep one instance across Gemini input-session reconnects.
//
// Interim events open the next logical utterance. Its FINAL and any immediate
// replayed FINAL retain that ID. A later interim opens a distinct turn even if
// the recognized text is identical. Without an interim or provider-supplied
// stable utterance identity, a final-only new utterance cannot be distinguished
// from replay of the preceding final; in that case this sequencer favors
// idempotent replay handling.
type LeadTurnSequencer struct {
	mu         sync.Mutex
	next       uint64
	activeID   string
	lastFinal  string
	activeDone bool
}

// NewLeadTurnSequencer creates a call-scoped owner. lastIssued is the greatest
// lead sequence already consumed by this call, such as one reconstructed from
// persisted ConversationState after reconnect.
func NewLeadTurnSequencer(lastIssued uint64) *LeadTurnSequencer {
	return &LeadTurnSequencer{next: lastIssued}
}

func (s *LeadTurnSequencer) assign(event TranscriptEvent) TranscriptEvent {
	if s == nil || (event.State != TranscriptInterim && event.State != TranscriptFinal) {
		return event
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.State == TranscriptInterim {
		if s.activeID == "" || s.activeDone {
			s.activeID = s.nextIDLocked()
			s.activeDone = false
		}
		event.TurnID = s.activeID
		return event
	}
	if s.activeID == "" {
		if s.lastFinal != "" {
			s.activeID = s.lastFinal
			s.activeDone = true
		} else {
			s.activeID = s.nextIDLocked()
			s.activeDone = true
			s.lastFinal = s.activeID
		}
	} else if !s.activeDone {
		s.activeDone = true
		s.lastFinal = s.activeID
	}
	event.TurnID = s.activeID
	return event
}

func (s *LeadTurnSequencer) nextIDLocked() string {
	s.next++
	return fmt.Sprintf("lead-%06d", s.next)
}

type leadTurnIdentityOwner interface {
	ownsLeadTurnIdentity()
	attachLeadTurnSequencer(*LeadTurnSequencer)
}

type leadTurnIdentitySession struct {
	InputTranscriberSession
	sequencer *LeadTurnSequencer
}

func (s *leadTurnIdentitySession) Receive(ctx context.Context) (TranscriptEvent, error) {
	event, err := s.InputTranscriberSession.Receive(ctx)
	if err != nil {
		return TranscriptEvent{}, err
	}
	return s.sequencer.assign(event), nil
}

func (*leadTurnIdentitySession) ownsLeadTurnIdentity() {}
func (s *leadTurnIdentitySession) attachLeadTurnSequencer(sequencer *LeadTurnSequencer) {
	if sequencer != nil {
		s.sequencer = sequencer
	}
}

// WithLeadTurnIdentity places a call-scoped application identity owner above
// an input session. It is safe to call for sessions already created by
// ConnectInputTranscriberWithTurnSequencer; those retain their existing owner.
func WithLeadTurnIdentity(session InputTranscriberSession, sequencer *LeadTurnSequencer) InputTranscriberSession {
	if session == nil {
		return nil
	}
	if owner, ok := session.(leadTurnIdentityOwner); ok {
		if sequencer != nil {
			owner.attachLeadTurnSequencer(sequencer)
		}
		return session
	}
	if sequencer == nil {
		sequencer = NewLeadTurnSequencer(0)
	}
	return &leadTurnIdentitySession{InputTranscriberSession: session, sequencer: sequencer}
}
