package conversation

import (
	"errors"
	"testing"
)

func TestConversationStateRecordsMinimalSignals(t *testing.T) {
	state, err := NewConversationState("conversation-1")
	if err != nil {
		t.Fatalf("create state: %v", err)
	}
	if err := state.RecordSignal(SignalOptedOut); err != nil {
		t.Fatalf("record opt-out: %v", err)
	}
	if got := state.Signals(); !got.OptedOut || got.LeadResponded {
		t.Fatalf("signals = %+v, want only opted out", got)
	}
	if err := state.RecordSignal(Signal("unsupported")); err == nil {
		t.Fatal("expected unsupported signal to be rejected")
	}
}

func TestConversationStateRejectsEmptyID(t *testing.T) {
	if _, err := NewConversationState(" "); !errors.Is(err, ErrInvalidConversationID) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConversationID)
	}
}
