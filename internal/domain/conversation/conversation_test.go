package conversation

import (
	"errors"
	"testing"
)

func TestConversationStateRecordsAndFinalizesPartialTurn(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	partial, err := NewTurn("turn-1", RoleLead, "bom dia", TranscriptPartial)
	if err != nil {
		t.Fatalf("create partial turn: %v", err)
	}
	if event, err := state.RecordTurn(partial); err != nil {
		t.Fatalf("record partial turn: %v", err)
	} else if event.Replaced {
		t.Fatal("first turn must not be marked as replacement")
	}
	final, err := NewTurn("turn-1", RoleLead, "bom dia, posso falar", TranscriptFinal)
	if err != nil {
		t.Fatalf("create final turn: %v", err)
	}
	if event, err := state.RecordTurn(final); err != nil {
		t.Fatalf("finalize turn: %v", err)
	} else if !event.Replaced {
		t.Fatal("partial-to-final update must be marked as replacement")
	}
	turns := state.Turns()
	if len(turns) != 1 || turns[0] != final {
		t.Fatalf("turns = %+v, want finalized turn", turns)
	}
	if !state.Signals().LeadResponded {
		t.Fatal("final lead turn must set LeadResponded")
	}
}

func TestConversationStateRejectsChangesAfterFinalization(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	final, err := NewTurn("turn-1", RoleAgent, "olá", TranscriptFinal)
	if err != nil {
		t.Fatalf("create final turn: %v", err)
	}
	if _, err := state.RecordTurn(final); err != nil {
		t.Fatalf("record final turn: %v", err)
	}
	changed, err := NewTurn("turn-1", RoleAgent, "conteúdo alterado", TranscriptFinal)
	if err != nil {
		t.Fatalf("create changed turn: %v", err)
	}
	if _, err := state.RecordTurn(changed); err == nil {
		t.Fatal("expected finalized turn to reject changes")
	} else {
		var finalized *FinalizedTurnError
		if !errors.As(err, &finalized) {
			t.Fatalf("error = %T, want *FinalizedTurnError", err)
		}
	}
}

func TestTurnValidation(t *testing.T) {
	tests := []struct {
		name, id   string
		role       ParticipantRole
		text       string
		transcript TranscriptState
		wantError  error
	}{
		{name: "missing id", role: RoleLead, text: "hello", transcript: TranscriptFinal, wantError: ErrInvalidTurn},
		{name: "missing text", id: "turn-1", role: RoleLead, transcript: TranscriptFinal, wantError: ErrInvalidTurn},
		{name: "invalid role", id: "turn-1", role: ParticipantRole("unknown"), text: "hello", transcript: TranscriptFinal, wantError: ErrInvalidTurn},
		{name: "invalid transcript state", id: "turn-1", role: RoleLead, text: "hello", transcript: TranscriptState("unknown"), wantError: ErrInvalidTurn},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewTurn(test.id, test.role, test.text, test.transcript); !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestConversationStageTransitionsAreDeterministic(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	for _, want := range []ConversationStage{StageActive, StageClosing, StageEnded} {
		event, err := state.Transition(want)
		if err != nil {
			t.Fatalf("transition to %s: %v", want, err)
		}
		if event.From == event.To || event.To != want {
			t.Fatalf("event = %+v, want destination %s", event, want)
		}
	}
	if _, err := state.Transition(StageActive); err == nil {
		t.Fatal("expected ended conversation to reject transition")
	}
}

func TestConversationStateReturnsCopiesOfTurns(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	turn, err := NewTurn("turn-1", RoleSystem, "context", TranscriptFinal)
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := state.RecordTurn(turn); err != nil {
		t.Fatalf("record turn: %v", err)
	}
	turns := state.Turns()
	turns[0] = Turn{}
	if state.Turns()[0] != turn {
		t.Fatal("state turns must not be mutable through returned slice")
	}
}
