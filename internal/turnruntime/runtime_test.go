package turnruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
)

type countingProvider struct {
	provider conversation.DecisionProvider
	mu       sync.Mutex
	calls    int
}

func (p *countingProvider) Decide(ctx context.Context, input conversation.DecisionInput) (conversation.Decision, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.provider.Decide(ctx, input)
}
func (p *countingProvider) Calls() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

type fakeDispatcher struct {
	mu     sync.Mutex
	calls  int
	seen   toolruntime.ToolExecutionRequest
	result conversation.ToolResult
	err    error
}

func (d *fakeDispatcher) Dispatch(ctx context.Context, request toolruntime.ToolExecutionRequest) (conversation.ToolResult, error) {
	d.mu.Lock()
	d.calls++
	d.seen = request
	d.mu.Unlock()
	if d.err != nil {
		return conversation.ToolResult{}, d.err
	}
	if err := ctx.Err(); err != nil {
		return conversation.ToolResult{}, err
	}
	return d.result, nil
}
func (d *fakeDispatcher) Calls() int { d.mu.Lock(); defer d.mu.Unlock(); return d.calls }
func (d *fakeDispatcher) Request() toolruntime.ToolExecutionRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seen
}

func runtimeState(t *testing.T, stage conversation.ConversationStage, optedOut bool) *conversation.ConversationState {
	t.Helper()
	state, err := conversation.NewConversationState("conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if stage != conversation.StageOpening {
		if _, err := state.Transition(conversation.StageActive); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := conversation.NewTurn("turn-1", conversation.RoleLead, "hello", conversation.TranscriptFinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordTurn(turn); err != nil {
		t.Fatal(err)
	}
	if optedOut {
		if err := state.RecordSignal(conversation.SignalOptedOut); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func scriptedProvider(t *testing.T, action conversation.NextAction, reason conversation.ReasonCode) *countingProvider {
	t.Helper()
	decision, err := conversation.NewDecision(action, reason)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := conversation.NewScriptedDecisionProvider([]conversation.Decision{decision})
	if err != nil {
		t.Fatal(err)
	}
	return &countingProvider{provider: provider}
}

func TestRuntimeReturnsNonCapabilityDirectiveWithoutDispatch(t *testing.T) {
	provider := scriptedProvider(t, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
	dispatcher := &fakeDispatcher{}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}

	directive, err := runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false)})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 || dispatcher.Calls() != 0 || directive.Kind != conversation.ActionAskQuestion || directive.Capability != "" {
		t.Fatalf("provider=%d dispatcher=%d directive=%+v", provider.Calls(), dispatcher.Calls(), directive)
	}
}

func TestRuntimeReturnsTerminalAndHandoffWithoutDispatch(t *testing.T) {
	for _, tc := range []struct {
		name              string
		action            conversation.NextAction
		reason            conversation.ReasonCode
		terminal, handoff bool
	}{
		{"end", conversation.ActionEndConversation, conversation.ReasonConversationComplete, true, false},
		{"handoff", conversation.ActionHandoff, conversation.ReasonHandoffRequired, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := scriptedProvider(t, tc.action, tc.reason)
			dispatcher := &fakeDispatcher{}
			runtime, err := New(provider, dispatcher)
			if err != nil {
				t.Fatal(err)
			}
			directive, err := runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false)})
			if err != nil || directive.Terminal != tc.terminal || directive.Handoff != tc.handoff || dispatcher.Calls() != 0 {
				t.Fatalf("directive=%+v error=%v calls=%d", directive, err, dispatcher.Calls())
			}
		})
	}
}

func TestRuntimeAllowsCapabilityAndBuildsFinalResultDirective(t *testing.T) {
	const name = tools.ToolCalendarCheckAvailability
	provider := scriptedProvider(t, conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired)
	dispatcher := &fakeDispatcher{result: conversation.ToolResult{Tool: name, Status: conversation.ToolResultSucceeded}}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	input := TurnInput{State: runtimeState(t, conversation.StageActive, false), Capability: &CapabilityContext{RequestedTool: name, Arguments: map[string]any{"date": "today"}, CorrelationID: "corr-1"}}

	directive, err := runtime.ProcessTurn(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 || dispatcher.Calls() != 1 || directive.ToolResult == nil || directive.ToolResult.Tool != name || directive.Capability != name {
		t.Fatalf("provider=%d dispatcher=%d directive=%+v", provider.Calls(), dispatcher.Calls(), directive)
	}
	if got := dispatcher.Request(); got.Directive.ToolResult != nil || got.Tool != name || got.CorrelationID != "corr-1" {
		t.Fatalf("request=%+v", got)
	}
}

func TestRuntimeDenyAndDeferReturnWithoutDispatch(t *testing.T) {
	cases := []struct {
		name, tool string
		stage      conversation.ConversationStage
		optedOut   bool
	}{
		{"deny opted out", tools.ToolWhatsAppSendMessage, conversation.StageActive, true},
		{"defer opening side effect", tools.ToolCalendarCreateEvent, conversation.StageOpening, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := scriptedProvider(t, conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired)
			dispatcher := &fakeDispatcher{}
			runtime, err := New(provider, dispatcher)
			if err != nil {
				t.Fatal(err)
			}
			directive, err := runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, tc.stage, tc.optedOut), Capability: &CapabilityContext{RequestedTool: tc.tool, Arguments: map[string]any{}}})
			if err != nil || directive.Executable || dispatcher.Calls() != 0 {
				t.Fatalf("directive=%+v error=%v calls=%d", directive, err, dispatcher.Calls())
			}
		})
	}
}

func TestRuntimeRejectsMissingAndUnexpectedCapabilityContext(t *testing.T) {
	provider := scriptedProvider(t, conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired)
	dispatcher := &fakeDispatcher{}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false)})
	if !errors.Is(err, ErrCapabilityContextRequired) || dispatcher.Calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, dispatcher.Calls())
	}

	provider = scriptedProvider(t, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
	runtime, err = New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false), Capability: &CapabilityContext{RequestedTool: tools.ToolCalendarCheckAvailability, Arguments: map[string]any{}}})
	if !errors.Is(err, ErrUnexpectedCapabilityContext) || dispatcher.Calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, dispatcher.Calls())
	}
}

func TestRuntimePropagatesDispatcherErrorWithoutRetryOrSecondDecision(t *testing.T) {
	provider := scriptedProvider(t, conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired)
	dispatcher := &fakeDispatcher{err: errors.New("executor failed")}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false), Capability: &CapabilityContext{RequestedTool: tools.ToolCalendarCheckAvailability, Arguments: map[string]any{}}})
	if !errors.Is(err, ErrToolDispatchFailed) || provider.Calls() != 1 || dispatcher.Calls() != 1 {
		t.Fatalf("error=%v provider=%d dispatcher=%d", err, provider.Calls(), dispatcher.Calls())
	}
}

func TestRuntimePreservesCancellationBeforeProviderAndDuringDispatch(t *testing.T) {
	provider := scriptedProvider(t, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
	dispatcher := &fakeDispatcher{}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runtime.ProcessTurn(ctx, TurnInput{State: runtimeState(t, conversation.StageActive, false)})
	if !errors.Is(err, context.Canceled) || provider.Calls() != 0 || dispatcher.Calls() != 0 {
		t.Fatalf("error=%v provider=%d dispatcher=%d", err, provider.Calls(), dispatcher.Calls())
	}

	provider = scriptedProvider(t, conversation.ActionRequestCapability, conversation.ReasonCapabilityRequired)
	dispatcher = &fakeDispatcher{err: context.Canceled}
	runtime, err = New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ProcessTurn(context.Background(), TurnInput{State: runtimeState(t, conversation.StageActive, false), Capability: &CapabilityContext{RequestedTool: tools.ToolCalendarCheckAvailability, Arguments: map[string]any{}}})
	if !errors.Is(err, context.Canceled) || provider.Calls() != 1 || dispatcher.Calls() != 1 {
		t.Fatalf("error=%v provider=%d dispatcher=%d", err, provider.Calls(), dispatcher.Calls())
	}
}

func TestRuntimePropagatesProviderExhaustionAndNilContext(t *testing.T) {
	provider := scriptedProvider(t, conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
	dispatcher := &fakeDispatcher{}
	runtime, err := New(provider, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	input := TurnInput{State: runtimeState(t, conversation.StageActive, false)}
	if _, err := runtime.ProcessTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ProcessTurn(context.Background(), input)
	if !errors.Is(err, conversation.ErrDecisionScriptExhausted) || dispatcher.Calls() != 0 {
		t.Fatalf("error=%v calls=%d", err, dispatcher.Calls())
	}
	if _, err := runtime.ProcessTurn(nil, input); !errors.Is(err, ErrInvalidTurnRuntime) {
		t.Fatalf("nil context error=%v", err)
	}
}
