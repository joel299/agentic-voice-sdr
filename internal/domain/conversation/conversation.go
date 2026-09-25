package conversation

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidConversationID = errors.New("conversation id must not be empty")
	ErrInvalidTurn           = errors.New("invalid conversation turn")
	ErrTurnConflict          = errors.New("conversation turn conflicts with existing turn")
)

type ParticipantRole string

const (
	RoleLead   ParticipantRole = "lead"
	RoleAgent  ParticipantRole = "agent"
	RoleSystem ParticipantRole = "system"
)

type TranscriptState string

const (
	TranscriptPartial TranscriptState = "partial"
	TranscriptFinal   TranscriptState = "final"
)

type ConversationStage string

const (
	StageOpening ConversationStage = "opening"
	StageActive  ConversationStage = "active"
	StageClosing ConversationStage = "closing"
	StageEnded   ConversationStage = "ended"
)

type Signal string

const (
	SignalLeadResponded Signal = "lead_responded"
	SignalOptedOut      Signal = "opted_out"
)

type Signals struct {
	LeadResponded bool
	OptedOut      bool
}

type Turn struct {
	ID         string
	Role       ParticipantRole
	Text       string
	Transcript TranscriptState
}

func NewTurn(id string, role ParticipantRole, text string, transcript TranscriptState) (Turn, error) {
	turn := Turn{ID: id, Role: role, Text: text, Transcript: transcript}
	if err := turn.validate(); err != nil {
		return Turn{}, err
	}
	return turn, nil
}

func (t Turn) validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Text) == "" || !validRole(t.Role) || !validTranscriptState(t.Transcript) {
		return ErrInvalidTurn
	}
	return nil
}

type TurnEvent struct {
	Turn     Turn
	Replaced bool
}

type StageTransition struct {
	From ConversationStage
	To   ConversationStage
}

type InvalidStageTransitionError struct {
	From ConversationStage
	To   ConversationStage
}

func (e *InvalidStageTransitionError) Error() string {
	return fmt.Sprintf("invalid conversation stage transition: %s -> %s", e.From, e.To)
}

type FinalizedTurnError struct {
	ID string
}

func (e *FinalizedTurnError) Error() string {
	return fmt.Sprintf("conversation turn %q is already finalized", e.ID)
}

type ConversationState struct {
	id      string
	stage   ConversationStage
	turns   []Turn
	signals Signals
}

func NewConversationState(id string) (*ConversationState, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidConversationID
	}
	return &ConversationState{id: id, stage: StageOpening}, nil
}

func (s *ConversationState) ID() string {
	return s.id
}

func (s *ConversationState) Stage() ConversationStage {
	return s.stage
}

func (s *ConversationState) Turns() []Turn {
	return append([]Turn(nil), s.turns...)
}

func (s *ConversationState) Signals() Signals {
	return s.signals
}

func (s *ConversationState) RecordTurn(turn Turn) (TurnEvent, error) {
	if err := turn.validate(); err != nil {
		return TurnEvent{}, err
	}
	for index, existing := range s.turns {
		if existing.ID != turn.ID {
			continue
		}
		if existing.Role != turn.Role {
			return TurnEvent{}, fmt.Errorf("%w: role for %q changed", ErrTurnConflict, turn.ID)
		}
		if existing.Transcript == TranscriptFinal {
			return TurnEvent{}, &FinalizedTurnError{ID: turn.ID}
		}
		s.turns[index] = turn
		s.updateSignals(turn)
		return TurnEvent{Turn: turn, Replaced: true}, nil
	}
	s.turns = append(s.turns, turn)
	s.updateSignals(turn)
	return TurnEvent{Turn: turn}, nil
}

func (s *ConversationState) RecordSignal(signal Signal) error {
	switch signal {
	case SignalLeadResponded:
		s.signals.LeadResponded = true
	case SignalOptedOut:
		s.signals.OptedOut = true
	default:
		return fmt.Errorf("invalid conversation signal: %q", signal)
	}
	return nil
}

func (s *ConversationState) updateSignals(turn Turn) {
	if turn.Role == RoleLead && turn.Transcript == TranscriptFinal {
		s.signals.LeadResponded = true
	}
}

func (s *ConversationState) Transition(to ConversationStage) (StageTransition, error) {
	if !validStageTransition(s.stage, to) {
		return StageTransition{}, &InvalidStageTransitionError{From: s.stage, To: to}
	}
	event := StageTransition{From: s.stage, To: to}
	s.stage = to
	return event, nil
}

func validRole(role ParticipantRole) bool {
	switch role {
	case RoleLead, RoleAgent, RoleSystem:
		return true
	default:
		return false
	}
}

func validTranscriptState(state TranscriptState) bool {
	return state == TranscriptPartial || state == TranscriptFinal
}

func validStageTransition(from, to ConversationStage) bool {
	switch from {
	case StageOpening:
		return to == StageActive || to == StageEnded
	case StageActive:
		return to == StageClosing || to == StageEnded
	case StageClosing:
		return to == StageEnded
	default:
		return false
	}
}
