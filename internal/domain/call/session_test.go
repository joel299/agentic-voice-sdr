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

func TestCallSessionRejectsInvalidAndTerminalTransitions(t *testing.T) {
	session := NewCallSession()
	if _, err := session.Transition(StateConnected); err == nil {
		t.Fatal("expected invalid transition error")
	} else {
		var invalid *InvalidTransitionError
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %T, want *InvalidTransitionError", err)
		}
	}

	for _, state := range []State{StateDialing, StateRinging, StateConnected, StateConversing, StateEnding, StateCompleted} {
		if _, err := session.Transition(state); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}
	if session.state != StateCompleted {
		t.Fatal("test setup did not reach terminal state")
	}
	if _, err := session.Transition(StateDialing); err == nil {
		t.Fatal("expected terminal state to reject transition")
	}
}

func TestCallSessionTerminalAlternatives(t *testing.T) {
	for _, terminal := range []State{StateNoAnswer, StateBusy, StateVoicemail, StateFailed, StateCanceled} {
		session := NewCallSession()
		if terminal == StateVoicemail {
			for _, state := range []State{StateDialing, StateRinging} {
				if _, err := session.Transition(state); err != nil {
					t.Fatal(err)
				}
			}
		} else if _, err := session.Transition(StateDialing); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Transition(terminal); err != nil {
			t.Fatalf("transition to %s: %v", terminal, err)
		}
		if session.State() != terminal {
			t.Fatalf("state = %s, want %s", session.State(), terminal)
		}
	}
}
