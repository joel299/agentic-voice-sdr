package conversation

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func responseGateState(t *testing.T, id string, turns ...Turn) *ConversationState {
	t.Helper()
	state, err := NewConversationState(id)
	if err != nil {
		t.Fatalf("NewConversationState: %v", err)
	}
	for _, turn := range turns {
		if _, err := state.RecordTurn(turn); err != nil {
			t.Fatalf("RecordTurn(%q): %v", turn.ID, err)
		}
	}
	return state
}

func responseGateTurn(t *testing.T, id string, role ParticipantRole, transcript TranscriptState) Turn {
	t.Helper()
	turn, err := NewTurn(id, role, "content", transcript)
	if err != nil {
		t.Fatalf("NewTurn: %v", err)
	}
	return turn
}

func responseGateDirective(t *testing.T) TurnDirective {
	t.Helper()
	state := responseGateState(t, "unused")
	input, err := NewDecisionInput(state)
	if err != nil {
		t.Fatalf("NewDecisionInput: %v", err)
	}
	decision, err := NewDecision(ActionAskQuestion, ReasonNeedsClarification)
	if err != nil {
		t.Fatalf("NewDecision: %v", err)
	}
	directive, err := BuildTurnDirective(OrchestratorInput{
		DecisionInput: input,
		Decision:      decision,
	})
	if err != nil {
		t.Fatalf("BuildTurnDirective: %v", err)
	}
	return directive
}

func TestResponseGateAuthorizesLatestFinalLead(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()

	auth, err := gate.Authorize(state, lead.ID, responseGateDirective(t))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if auth.Key.ConversationID != "conversation-1" || auth.Key.SourceTurnID != lead.ID {
		t.Fatalf("unexpected response key: %+v", auth.Key)
	}
	if auth.State != ResponseAuthorized {
		t.Fatalf("state = %q, want %q", auth.State, ResponseAuthorized)
	}
}

func TestResponseGateRejectsNonFinalOrNonLeadSources(t *testing.T) {
	tests := []struct {
		name string
		role ParticipantRole
		tx   TranscriptState
	}{
		{"partial lead", RoleLead, TranscriptPartial},
		{"agent", RoleAgent, TranscriptFinal},
		{"system", RoleSystem, TranscriptFinal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn := responseGateTurn(t, "source", tt.role, tt.tx)
			gate := NewResponseGate()
			_, err := gate.Authorize(responseGateState(t, "conversation-1", turn), turn.ID, responseGateDirective(t))
			if !errors.Is(err, ErrInvalidResponseSource) {
				t.Fatalf("error = %v, want ErrInvalidResponseSource", err)
			}
		})
	}
}

func TestResponseGateRejectsUnknownAndStaleSources(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first, second)
	gate := NewResponseGate()

	_, err := gate.Authorize(state, "missing", responseGateDirective(t))
	if !errors.Is(err, ErrInvalidResponseSource) {
		t.Fatalf("unknown source error = %v", err)
	}
	_, err = gate.Authorize(state, first.ID, responseGateDirective(t))
	if !errors.Is(err, ErrStaleResponseTurn) {
		t.Fatalf("stale source error = %v", err)
	}
	if _, err := gate.Authorize(state, second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("latest source rejected: %v", err)
	}
}

func TestResponseGateRejectsInvalidDirectiveAndDuplicateAuthorization(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()

	_, err := gate.Authorize(state, lead.ID, TurnDirective{})
	if !errors.Is(err, ErrInvalidResponseDirective) {
		t.Fatalf("invalid directive error = %v", err)
	}
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first authorization: %v", err)
	}
	_, err = gate.Authorize(state, lead.ID, responseGateDirective(t))
	if !errors.Is(err, ErrResponseAlreadyAuthorized) {
		t.Fatalf("duplicate authorization error = %v", err)
	}
}

func TestResponseGateStateTransitions(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}
	gate := NewResponseGate()

	if err := gate.Start(key); !errors.Is(err, ErrResponseNotAuthorized) {
		t.Fatalf("start before authorize = %v", err)
	}
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := gate.Start(key); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := gate.Start(key); !errors.Is(err, ErrResponseAlreadyStarted) {
		t.Fatalf("duplicate start = %v", err)
	}
	if err := gate.Complete(ResponseKey{ConversationID: "other", SourceTurnID: "lead"}); !errors.Is(err, ErrResponseNotAuthorized) {
		t.Fatalf("complete unknown = %v", err)
	}
	if err := gate.Complete(key); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := gate.Complete(key); !errors.Is(err, ErrResponseAlreadyCompleted) {
		t.Fatalf("duplicate complete = %v", err)
	}
}

func TestResponseGateRejectsCompleteBeforeStart(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}
	if err := gate.Complete(key); !errors.Is(err, ErrResponseNotStarted) {
		t.Fatalf("complete before start = %v", err)
	}
}

func TestResponseGateAllowsNextFinalLeadAfterCompletion(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first)
	gate := NewResponseGate()
	if _, err := gate.Authorize(state, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: first.ID}
	if err := gate.Start(key); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := gate.Complete(key); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := state.RecordTurn(second); err != nil {
		t.Fatalf("RecordTurn second: %v", err)
	}
	if _, err := gate.Authorize(state, second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("second Authorize: %v", err)
	}
}

func TestResponseGateStateDoesNotStoreTranscriptOrProviderPayload(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(ResponseGate{}), reflect.TypeOf(ResponseAuthorization{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			if name == "Text" || name == "Transcript" || name == "Audio" || name == "Provider" || name == "Payload" {
				t.Fatalf("%s stores forbidden field %s", typ.Name(), name)
			}
		}
	}
}

func TestResponseGateAuthorizeIsConcurrencySafe(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	duplicates := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := gate.Authorize(state, lead.ID, responseGateDirective(t))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if errors.Is(err, ErrResponseAlreadyAuthorized) {
				duplicates++
			} else {
				t.Errorf("Authorize: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 || duplicates != 1 {
		t.Fatalf("successes=%d duplicates=%d, want 1 each", successes, duplicates)
	}
}
