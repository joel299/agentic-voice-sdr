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

func TestResponseGateRejectsSecondAuthorizedCycleForConversation(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first)
	gate := NewResponseGate()

	if _, err := gate.Authorize(state, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	if _, err := state.RecordTurn(second); err != nil {
		t.Fatalf("RecordTurn second: %v", err)
	}
	_, err := gate.Authorize(state, second.ID, responseGateDirective(t))
	if !errors.Is(err, ErrResponseCycleActive) {
		t.Fatalf("second authorization = %v, want ErrResponseCycleActive", err)
	}
}

func TestResponseGateRejectsSecondStartedCycleForConversation(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first)
	gate := NewResponseGate()

	if _, err := gate.Authorize(state, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	firstKey := ResponseKey{ConversationID: state.ID(), SourceTurnID: first.ID}
	if err := gate.Start(firstKey); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := state.RecordTurn(second); err != nil {
		t.Fatalf("RecordTurn second: %v", err)
	}
	_, err := gate.Authorize(state, second.ID, responseGateDirective(t))
	if !errors.Is(err, ErrResponseCycleActive) {
		t.Fatalf("second authorization = %v, want ErrResponseCycleActive", err)
	}
}

func TestResponseGateAllowsNextCycleOnlyAfterCompletion(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first)
	gate := NewResponseGate()

	if _, err := gate.Authorize(state, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	firstKey := ResponseKey{ConversationID: state.ID(), SourceTurnID: first.ID}
	if err := gate.Start(firstKey); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := gate.Complete(firstKey); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if _, err := state.RecordTurn(second); err != nil {
		t.Fatalf("RecordTurn second: %v", err)
	}
	if _, err := gate.Authorize(state, second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("second Authorize: %v", err)
	}
}

func TestResponseGateAllowsDifferentConversationsConcurrently(t *testing.T) {
	first := responseGateTurn(t, "lead-a", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-b", RoleLead, TranscriptFinal)
	gate := NewResponseGate()

	if _, err := gate.Authorize(responseGateState(t, "conversation-a", first), first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("conversation A Authorize: %v", err)
	}
	if _, err := gate.Authorize(responseGateState(t, "conversation-b", second), second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("conversation B Authorize: %v", err)
	}
}

func TestResponseGateConcurrentDistinctKeysSameConversationHasOneActiveCycle(t *testing.T) {
	first := responseGateTurn(t, "lead-a", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-b", RoleLead, TranscriptFinal)
	stateA := responseGateState(t, "conversation-1", first)
	stateB := responseGateState(t, "conversation-1", second)
	gate := NewResponseGate()

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	cycleErrors := 0
	for _, input := range []struct {
		state *ConversationState
		id    string
	}{
		{stateA, first.ID},
		{stateB, second.ID},
	} {
		wg.Add(1)
		go func(state *ConversationState, id string) {
			defer wg.Done()
			_, err := gate.Authorize(state, id, responseGateDirective(t))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if errors.Is(err, ErrResponseCycleActive) {
				cycleErrors++
			} else {
				t.Errorf("Authorize: %v", err)
			}
		}(input.state, input.id)
	}
	wg.Wait()
	if successes != 1 || cycleErrors != 1 {
		t.Fatalf("successes=%d cycleErrors=%d, want 1 each", successes, cycleErrors)
	}
}

func TestResponseGateFailsAuthorizedCycle(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}
	if err := gate.Fail(key); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := gate.cycles[key]; got != ResponseFailed {
		t.Fatalf("state = %q, want %q", got, ResponseFailed)
	}
}

func TestResponseGateFailsStartedCycle(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}
	if err := gate.Start(key); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := gate.Fail(key); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := gate.cycles[key]; got != ResponseFailed {
		t.Fatalf("state = %q, want %q", got, ResponseFailed)
	}
}

func TestResponseGateRejectsInvalidFailureTransitions(t *testing.T) {
	lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", lead)
	gate := NewResponseGate()
	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}

	if err := gate.Fail(key); !errors.Is(err, ErrResponseNotAuthorized) {
		t.Fatalf("Fail before Authorize = %v", err)
	}
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := gate.Fail(key); err != nil {
		t.Fatalf("first Fail: %v", err)
	}
	if err := gate.Fail(key); !errors.Is(err, ErrResponseAlreadyFailed) {
		t.Fatalf("second Fail = %v", err)
	}
	if err := gate.Start(key); !errors.Is(err, ErrResponseAlreadyFailed) {
		t.Fatalf("Start after Fail = %v", err)
	}
	if err := gate.Complete(key); !errors.Is(err, ErrResponseAlreadyFailed) {
		t.Fatalf("Complete after Fail = %v", err)
	}
	if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); !errors.Is(err, ErrResponseAlreadyAuthorized) {
		t.Fatalf("same-key reauthorization = %v", err)
	}
}

func TestResponseGateFailedCycleDoesNotBlockNextLead(t *testing.T) {
	first := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-2", RoleLead, TranscriptFinal)
	state := responseGateState(t, "conversation-1", first)
	gate := NewResponseGate()
	if _, err := gate.Authorize(state, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("first Authorize: %v", err)
	}
	firstKey := ResponseKey{ConversationID: state.ID(), SourceTurnID: first.ID}
	if err := gate.Fail(firstKey); err != nil {
		t.Fatalf("first Fail: %v", err)
	}
	if _, err := state.RecordTurn(second); err != nil {
		t.Fatalf("RecordTurn second: %v", err)
	}
	if _, err := gate.Authorize(state, second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("second Authorize: %v", err)
	}
}

func TestResponseGateFailedCycleDoesNotBlockDifferentConversation(t *testing.T) {
	first := responseGateTurn(t, "lead-a", RoleLead, TranscriptFinal)
	second := responseGateTurn(t, "lead-b", RoleLead, TranscriptFinal)
	gate := NewResponseGate()
	stateA := responseGateState(t, "conversation-a", first)
	stateB := responseGateState(t, "conversation-b", second)
	if _, err := gate.Authorize(stateA, first.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("conversation A Authorize: %v", err)
	}
	if err := gate.Fail(ResponseKey{ConversationID: stateA.ID(), SourceTurnID: first.ID}); err != nil {
		t.Fatalf("conversation A Fail: %v", err)
	}
	if _, err := gate.Authorize(stateB, second.ID, responseGateDirective(t)); err != nil {
		t.Fatalf("conversation B Authorize: %v", err)
	}
}

func TestResponseGateConcurrentCompleteAndFailHasOneTerminalWinner(t *testing.T) {
	for iteration := 0; iteration < 25; iteration++ {
		lead := responseGateTurn(t, "lead-1", RoleLead, TranscriptFinal)
		state := responseGateState(t, "conversation-1", lead)
		gate := NewResponseGate()
		if _, err := gate.Authorize(state, lead.ID, responseGateDirective(t)); err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		key := ResponseKey{ConversationID: state.ID(), SourceTurnID: lead.ID}
		if err := gate.Start(key); err != nil {
			t.Fatalf("Start: %v", err)
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		successes := 0
		for _, action := range []func() error{
			func() error { return gate.Complete(key) },
			func() error { return gate.Fail(key) },
		} {
			wg.Add(1)
			go func(action func() error) {
				defer wg.Done()
				if err := action(); err == nil {
					mu.Lock()
					successes++
					mu.Unlock()
				} else if !errors.Is(err, ErrResponseAlreadyCompleted) && !errors.Is(err, ErrResponseAlreadyFailed) {
					t.Errorf("terminal transition error = %v", err)
				}
			}(action)
		}
		wg.Wait()
		if successes != 1 {
			t.Fatalf("iteration %d successes = %d, want 1", iteration, successes)
		}
		finalState := gate.cycles[key]
		if finalState != ResponseCompleted && finalState != ResponseFailed {
			t.Fatalf("iteration %d final state = %q, want terminal state", iteration, finalState)
		}
	}
}
