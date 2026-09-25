package conversation

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func scriptedTestInput() DecisionInput {
	return DecisionInput{
		Stage:               StageActive,
		Signals:             Signals{LeadResponded: true},
		TurnCount:           1,
		LastTurnRole:        RoleLead,
		LastTranscriptState: TranscriptFinal,
	}
}

func scriptedTestDecisions(t *testing.T) []Decision {
	t.Helper()
	first, err := NewDecision(ActionAskQuestion, ReasonNeedsClarification)
	if err != nil {
		t.Fatalf("create first decision: %v", err)
	}
	second, err := NewDecision(ActionEndConversation, ReasonConversationComplete)
	if err != nil {
		t.Fatalf("create second decision: %v", err)
	}
	return []Decision{first, second}
}

func TestScriptedDecisionProviderReturnsScriptedDecisionsInOrder(t *testing.T) {
	provider, err := NewScriptedDecisionProvider(scriptedTestDecisions(t))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	first, err := provider.Decide(context.Background(), scriptedTestInput())
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	second, err := provider.Decide(context.Background(), scriptedTestInput())
	if err != nil {
		t.Fatalf("second decision: %v", err)
	}
	if first.NextAction != ActionAskQuestion || first.Reason != ReasonNeedsClarification {
		t.Fatalf("first = %+v, want ask question", first)
	}
	if second.NextAction != ActionEndConversation || second.Reason != ReasonConversationComplete {
		t.Fatalf("second = %+v, want end conversation", second)
	}
}

func TestScriptedDecisionProviderReturnsStableExhaustionError(t *testing.T) {
	provider, err := NewScriptedDecisionProvider(scriptedTestDecisions(t))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for range 2 {
		if _, err := provider.Decide(context.Background(), scriptedTestInput()); err != nil {
			t.Fatalf("consume scripted decision: %v", err)
		}
	}
	_, err = provider.Decide(context.Background(), scriptedTestInput())
	if !errors.Is(err, ErrDecisionScriptExhausted) {
		t.Fatalf("error = %v, want %v", err, ErrDecisionScriptExhausted)
	}
}

func TestScriptedDecisionProviderRejectsInvalidScript(t *testing.T) {
	invalid := Decision{NextAction: NextAction("unknown"), Reason: ReasonNeedsClarification}
	if _, err := NewScriptedDecisionProvider([]Decision{invalid}); !errors.Is(err, ErrInvalidDecisionScript) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidDecisionScript)
	}
}

func TestScriptedDecisionProviderCancellationDoesNotConsumeDecision(t *testing.T) {
	provider, err := NewScriptedDecisionProvider(scriptedTestDecisions(t))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provider.Decide(ctx, scriptedTestInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
	decision, err := provider.Decide(context.Background(), scriptedTestInput())
	if err != nil {
		t.Fatalf("decision after cancellation: %v", err)
	}
	if decision.NextAction != ActionAskQuestion {
		t.Fatalf("decision = %+v, canceled call consumed script entry", decision)
	}
}

func TestScriptedDecisionProviderReplaysDeterministically(t *testing.T) {
	script := scriptedTestDecisions(t)
	first, err := NewScriptedDecisionProvider(script)
	if err != nil {
		t.Fatalf("create first provider: %v", err)
	}
	second, err := NewScriptedDecisionProvider(script)
	if err != nil {
		t.Fatalf("create second provider: %v", err)
	}
	for range len(script) {
		gotFirst, err := first.Decide(context.Background(), scriptedTestInput())
		if err != nil {
			t.Fatalf("first provider: %v", err)
		}
		gotSecond, err := second.Decide(context.Background(), scriptedTestInput())
		if err != nil {
			t.Fatalf("second provider: %v", err)
		}
		if gotFirst != gotSecond {
			t.Fatalf("decisions differ: first=%+v second=%+v", gotFirst, gotSecond)
		}
	}
}

func TestScriptedDecisionProviderSupportsConcurrentCalls(t *testing.T) {
	decision, err := NewDecision(ActionAskQuestion, ReasonNeedsClarification)
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	provider, err := NewScriptedDecisionProvider([]Decision{decision, decision, decision, decision})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := provider.Decide(context.Background(), scriptedTestInput())
			errs <- callErr
		}()
	}
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent decision: %v", callErr)
		}
	}
	_, err = provider.Decide(context.Background(), scriptedTestInput())
	if !errors.Is(err, ErrDecisionScriptExhausted) {
		t.Fatalf("error = %v, want %v after concurrent consumption", err, ErrDecisionScriptExhausted)
	}
}

var _ DecisionProvider = (*ScriptedDecisionProvider)(nil)
