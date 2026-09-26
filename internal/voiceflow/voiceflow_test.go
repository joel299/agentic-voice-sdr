package voiceflow

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/turnloop"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

type testProcessor struct {
	mu        sync.Mutex
	calls     int
	inputs    []turnruntime.TurnInput
	directive conversation.TurnDirective
	err       error
}

func (p *testProcessor) ProcessTurn(_ context.Context, input turnruntime.TurnInput) (conversation.TurnDirective, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.inputs = append(p.inputs, input)
	return p.directive, p.err
}

type testResponder struct{ calls int }

func (r *testResponder) SendTurnDirective(context.Context, conversation.TurnDirective) error {
	r.calls++
	return nil
}

func newTestHandler(t *testing.T, processorErr error) (*FinalTranscriptHandler, *conversation.ConversationState, *testProcessor) {
	t.Helper()
	state, err := conversation.NewConversationState("conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	processor := &testProcessor{
		err:       processorErr,
		directive: conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification},
	}
	coordinator, err := turnloop.New(processor, &testResponder{}, conversation.NewResponseGate())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewFinalTranscriptHandler(state, coordinator, nil)
	if err != nil {
		t.Fatal(err)
	}
	return handler, state, processor
}

func finalEvent(text string) geminilive.Event {
	return geminilive.Event{Kind: geminilive.EventInputTranscription, InputTranscriptState: geminilive.TranscriptFinal, Text: text}
}

func interimEvent(text string) geminilive.Event {
	return geminilive.Event{Kind: geminilive.EventInputTranscription, InputTranscriptState: geminilive.TranscriptInterim, Text: text}
}

func TestInterimAndBlankFinalDoNotBegin(t *testing.T) {
	h, state, processor := newTestHandler(t, nil)
	if err := h.HandleEvent(context.Background(), interimEvent("partial")); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleEvent(context.Background(), finalEvent("   ")); err != nil {
		t.Fatal(err)
	}
	if processor.calls != 0 || len(state.Turns()) != 0 {
		t.Fatalf("calls=%d turns=%d, want zero", processor.calls, len(state.Turns()))
	}
}

func TestFinalTranscriptCreatesSequentialLeadTurnsIncludingDuplicates(t *testing.T) {
	h, state, processor := newTestHandler(t, nil)
	if err := h.HandleEvent(context.Background(), finalEvent("same phrase")); err != nil {
		t.Fatal(err)
	}
	lease := h.Lifecycle().CaptureActive()
	if lease == nil || !lease.ModelAudioAuthorized() {
		t.Fatal("successful Begin did not bind an authorized lifecycle lease")
	}
	if err := lease.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleEvent(context.Background(), finalEvent("same phrase")); err != nil {
		t.Fatal(err)
	}
	turns := state.Turns()
	if len(turns) != 2 || turns[0].ID != "lead-000001" || turns[1].ID != "lead-000002" {
		t.Fatalf("turns = %#v", turns)
	}
	for _, turn := range turns {
		if turn.Role != conversation.RoleLead || turn.Transcript != conversation.TranscriptFinal || turn.Text != "same phrase" {
			t.Fatalf("turn = %#v", turn)
		}
	}
	if processor.calls != 2 {
		t.Fatalf("Begin/process calls=%d, want 2", processor.calls)
	}
}

func TestFinalTranscriptIsConsumedAndNormalized(t *testing.T) {
	h, state, processor := newTestHandler(t, nil)
	downstreamCalls := 0
	h.downstream = func(context.Context, geminilive.Event) error {
		downstreamCalls++
		return nil
	}
	if err := h.HandleEvent(context.Background(), finalEvent("   hello world   ")); err != nil {
		t.Fatal(err)
	}
	turns := state.Turns()
	if len(turns) != 1 || turns[0].Text != "hello world" {
		t.Fatalf("turns=%#v, want normalized text", turns)
	}
	if processor.calls != 1 || downstreamCalls != 0 {
		t.Fatalf("processor calls=%d downstream calls=%d", processor.calls, downstreamCalls)
	}
	if err := h.HandleEvent(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if downstreamCalls != 1 {
		t.Fatalf("downstream calls=%d, want 1 for non-final event", downstreamCalls)
	}
}

func TestBlankFinalIsConsumedWithoutDownstream(t *testing.T) {
	h, _, processor := newTestHandler(t, nil)
	downstreamCalls := 0
	h.downstream = func(context.Context, geminilive.Event) error {
		downstreamCalls++
		return nil
	}
	if err := h.HandleEvent(context.Background(), finalEvent(" 	\n ")); err != nil {
		t.Fatal(err)
	}
	if processor.calls != 0 || downstreamCalls != 0 {
		t.Fatalf("processor calls=%d downstream calls=%d", processor.calls, downstreamCalls)
	}
}

func TestBeginErrorLeavesNoActiveLifecycle(t *testing.T) {
	beginErr := errors.New("processor failed")
	h, state, processor := newTestHandler(t, beginErr)
	if err := h.HandleEvent(context.Background(), finalEvent("hello")); !errors.Is(err, beginErr) {
		t.Fatalf("error=%v, want %v", err, beginErr)
	}
	if processor.calls != 1 || len(state.Turns()) != 1 {
		t.Fatalf("calls=%d turns=%d", processor.calls, len(state.Turns()))
	}
	if lease := h.Lifecycle().CaptureActive(); lease != nil {
		t.Fatal("failed Begin bound an active lifecycle")
	}
}

func TestNonTranscriptEventForwardsDownstream(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	var got []geminilive.EventKind
	h.downstream = func(_ context.Context, event geminilive.Event) error {
		got = append(got, event.Kind)
		return nil
	}
	if err := h.HandleEvent(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != geminilive.EventTurnComplete {
		t.Fatalf("downstream events=%v", got)
	}
}

type lifecycleCoordinator struct {
	mu    sync.Mutex
	calls []string
}

func (c *lifecycleCoordinator) Complete(key conversation.ResponseKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "complete:"+key.SourceTurnID)
	return nil
}
func (c *lifecycleCoordinator) Fail(key conversation.ResponseKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "fail:"+key.SourceTurnID)
	return nil
}

func TestStaleLeaseCannotCompleteOrFailNewKey(t *testing.T) {
	coordinator := &lifecycleCoordinator{}
	adapter := NewResponseLifecycleAdapter(coordinator)
	keyA := conversation.ResponseKey{ConversationID: "c", SourceTurnID: "a"}
	keyB := conversation.ResponseKey{ConversationID: "c", SourceTurnID: "b"}
	adapter.Bind(keyA)
	leaseA := adapter.CaptureActive()
	adapter.Bind(keyB)
	if leaseA.ModelAudioAuthorized() {
		t.Fatal("stale lease authorized model audio")
	}
	if err := leaseA.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := leaseA.Fail(context.Background(), errors.New("provider detail must not persist")); err != nil {
		t.Fatal(err)
	}
	leaseB := adapter.CaptureActive()
	if leaseB == nil || !leaseB.ModelAudioAuthorized() {
		t.Fatal("new key was cleared by stale lease")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.calls) != 0 {
		t.Fatalf("stale lease called coordinator: %v", coordinator.calls)
	}
}

func TestFailActiveSnapshotsAndClearsCurrentKey(t *testing.T) {
	coordinator := &lifecycleCoordinator{}
	adapter := NewResponseLifecycleAdapter(coordinator)
	key := conversation.ResponseKey{ConversationID: "c", SourceTurnID: "a"}
	adapter.Bind(key)
	if err := adapter.FailActive(context.Background(), errors.New("provider reason")); err != nil {
		t.Fatal(err)
	}
	if adapter.CaptureActive() != nil {
		t.Fatal("FailActive did not clear current key")
	}
}

func TestCurrentLeaseOperationsUseExactCurrentKey(t *testing.T) {
	coordinator := &lifecycleCoordinator{}
	adapter := NewResponseLifecycleAdapter(coordinator)
	keyA := conversation.ResponseKey{ConversationID: "c", SourceTurnID: "a"}
	keyB := conversation.ResponseKey{ConversationID: "c", SourceTurnID: "b"}
	adapter.Bind(keyA)
	leaseA := adapter.CaptureActive()
	if err := leaseA.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	adapter.Bind(keyB)
	if err := adapter.FailActive(context.Background(), errors.New("provider reason")); err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.calls) != 2 || coordinator.calls[0] != "complete:a" || coordinator.calls[1] != "fail:b" {
		t.Fatalf("calls=%v", coordinator.calls)
	}
}

func TestResponseLifecycleAdapterConcurrentOperations(t *testing.T) {
	coordinator := &lifecycleCoordinator{}
	adapter := NewResponseLifecycleAdapter(coordinator)
	keys := []conversation.ResponseKey{
		{ConversationID: "c", SourceTurnID: "a"},
		{ConversationID: "c", SourceTurnID: "b"},
		{ConversationID: "c", SourceTurnID: "c"},
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := keys[i%len(keys)]
			adapter.Bind(key)
			lease := adapter.CaptureActive()
			if lease != nil {
				_ = lease.ModelAudioAuthorized()
				if i%2 == 0 {
					_ = lease.Complete(context.Background())
				} else {
					_ = lease.Fail(context.Background(), errors.New("provider detail"))
				}
			}
			_ = adapter.FailActive(context.Background(), errors.New("session detail"))
		}(i)
	}
	wg.Wait()
	coordinator.mu.Lock()
	coordinator.calls = nil
	coordinator.mu.Unlock()
	adapter.Bind(keys[0])
	lease := adapter.CaptureActive()
	if lease == nil || !lease.ModelAudioAuthorized() {
		t.Fatal("final active key is not authorized")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for _, call := range coordinator.calls {
		if call == "complete:"+keys[0].SourceTurnID || call == "fail:"+keys[0].SourceTurnID {
			t.Fatalf("concurrent stale operation touched final key: %s", call)
		}
	}
}
