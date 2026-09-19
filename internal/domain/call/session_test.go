package call

import (
	"errors"
	"testing"
	"time"
)

func TestCallSessionHappyPath(t *testing.T) {
	stamp := time.Date(2026, 9, 18, 22, 0, 0, 0, time.UTC)
	session := NewCallSession(func() time.Time { return stamp })
	if got := session.State(); got != StateCreated {
		t.Fatalf("initial state = %q, want %q", got, StateCreated)
	}

	for _, want := range []State{StateDialing, StateRinging, StateConnected, StateConversing, StateEnding, StateCompleted} {
		event, err := session.Transition(want)
		if err != nil {
			t.Fatalf("transition to %s: %v", want, err)
		}
		if event.To != want || event.OccurredAt != stamp {
			t.Fatalf("event = %+v, want destination %q at %s", event, want, stamp)
		}
	}
}

func TestCallSessionRejectsInvalidTransition(t *testing.T) {
	session := NewCallSession()
	if _, err := session.Transition(StateConnected); err == nil {
		t.Fatal("expected invalid transition error")
	} else {
		var invalid *InvalidTransitionError
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %T, want *InvalidTransitionError", err)
		}
	}
}

func TestCallSessionTerminalStatesRejectActiveTransitions(t *testing.T) {
	tests := []struct {
		name     string
		terminal State
		setup    []State
	}{
		{name: "COMPLETED", terminal: StateCompleted, setup: []State{StateDialing, StateRinging, StateConnected, StateConversing, StateEnding}},
		{name: "NO_ANSWER", terminal: StateNoAnswer, setup: []State{StateDialing}},
		{name: "BUSY", terminal: StateBusy, setup: []State{StateDialing}},
		{name: "VOICEMAIL", terminal: StateVoicemail, setup: []State{StateDialing, StateRinging}},
		{name: "FAILED", terminal: StateFailed, setup: []State{StateDialing}},
		{name: "CANCELED", terminal: StateCanceled, setup: []State{StateDialing}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := NewCallSession()
			for _, state := range test.setup {
				if _, err := session.Transition(state); err != nil {
					t.Fatalf("transition to %s: %v", state, err)
				}
			}
			if _, err := session.Transition(test.terminal); err != nil {
				t.Fatalf("transition to %s: %v", test.terminal, err)
			}

			if _, err := session.Transition(StateDialing); err == nil {
				t.Fatal("expected terminal state to reject transition to active state")
			} else {
				var invalid *InvalidTransitionError
				if !errors.As(err, &invalid) {
					t.Fatalf("error = %T, want *InvalidTransitionError", err)
				}
			}
			if session.State() != test.terminal {
				t.Fatalf("state = %s, want %s", session.State(), test.terminal)
			}
		})
	}
}
