package geminilive

import (
	"context"
	"fmt"
	"sync"
)

// LeadTurnSequencer owns monotonically increasing IDs for one application call.
// Keep one instance across Gemini input-session reconnects.
//
// Interim events open or continue an application utterance. Its first FINAL
// closes that turn. Any subsequent FINAL without an explicit application
// identity opens a fresh turn, even if its text is identical. The provider does
// not supply a stable utterance identity here, so final-only delivery replay
// cannot safely be distinguished from a legitimate new utterance.
type LeadTurnSequencer struct {
	mu         sync.Mutex
	next       uint64
	activeID   string
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
	if s.activeID == "" || s.activeDone {
		s.activeID = s.nextIDLocked()
	}
	s.activeDone = true
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
