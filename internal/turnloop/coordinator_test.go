package turnloop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

var errProcessor = errors.New("processor failed")
var errResponder = errors.New("responder failed")

func coordinatorState(t *testing.T, id, turnID string) *conversation.ConversationState {
	t.Helper()
	state, err := conversation.NewConversationState(id)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NewTurn(turnID, conversation.RoleLead, "final lead turn", conversation.TranscriptFinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordTurn(turn); err != nil {
		t.Fatal(err)
	}
	return state
}

func validDirective() conversation.TurnDirective {
	return conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}
}

type processorSpy struct {
	calls     atomic.Int32
	entered   chan struct{}
	release   chan struct{}
	directive conversation.TurnDirective
	err       error
	mu        sync.Mutex
	inputs    []turnruntime.TurnInput
}

func (p *processorSpy) ProcessTurn(ctx context.Context, input turnruntime.TurnInput) (conversation.TurnDirective, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.inputs = append(p.inputs, input)
	p.mu.Unlock()
	if p.entered != nil {
		close(p.entered)
		p.entered = nil
	}
	if p.release != nil {
		<-p.release
	}
	return p.directive, p.err
}

type responderSpy struct {
	calls atomic.Int32
	err   error
	mu    sync.Mutex
	order *[]string
}

func (r *responderSpy) SendTurnDirective(context.Context, conversation.TurnDirective) error {
	r.calls.Add(1)
	if r.order != nil {
		r.mu.Lock()
		*r.order = append(*r.order, "send")
		r.mu.Unlock()
	}
	return r.err
}

func newCoordinator(t *testing.T, p *processorSpy, r *responderSpy) *Coordinator {
	t.Helper()
	c, err := New(p, r, conversation.NewResponseGate())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBeginNoToolReservesBeforeRuntimeAndLeavesStarted(t *testing.T) {
	order := []string{}
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{order: &order}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")

	key, err := c.Begin(context.Background(), state, "turn-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key.ConversationID != "conversation-1" || key.SourceTurnID != "turn-1" {
		t.Fatalf("unexpected key: %+v", key)
	}
	if p.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("calls = processor %d responder %d", p.calls.Load(), r.calls.Load())
	}
	if err := c.Complete(key); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := c.Complete(key); !errors.Is(err, conversation.ErrResponseAlreadyCompleted) {
		t.Fatalf("duplicate Complete err = %v", err)
	}
}

func TestBeginRejectsSequentialDuplicateBeforeRuntime(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err == nil {
		t.Fatal("duplicate Begin succeeded")
	}
	if p.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("duplicate caused calls: processor %d responder %d", p.calls.Load(), r.calls.Load())
	}
}

func TestBeginCompetingSourceSameConversationRejected(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err != nil {
		t.Fatal(err)
	}
	turn2, err := conversation.NewTurn("turn-2", conversation.RoleLead, "new final turn", conversation.TranscriptFinal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordTurn(turn2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Begin(context.Background(), state, "turn-2", nil); err == nil {
		t.Fatal("competing source succeeded")
	}
	if p.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("calls = processor %d responder %d", p.calls.Load(), r.calls.Load())
	}
}

func TestBeginCapabilityPathPassesContextWithoutChoosingTool(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	capability := &turnruntime.CapabilityContext{RequestedTool: "calendar.check_availability", Arguments: map[string]any{"date": "today"}, CorrelationID: "corr-1"}
	if _, err := c.Begin(context.Background(), state, "turn-1", capability); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	got := p.inputs[0].Capability
	p.mu.Unlock()
	if got != capability {
		t.Fatalf("capability pointer was not passed through: %#v", got)
	}
}

func TestBeginConcurrentDuplicateAllowsOneRuntimeAndSend(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	p := &processorSpy{directive: validDirective(), entered: entered, release: release}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	results := make(chan error, 2)
	go func() { _, err := c.Begin(context.Background(), state, "turn-1", nil); results <- err }()
	<-entered
	go func() { _, err := c.Begin(context.Background(), state, "turn-1", nil); results <- err }()
	second := <-results
	if second == nil {
		t.Fatal("concurrent duplicate succeeded")
	}
	close(release)
	if first := <-results; first != nil {
		t.Fatalf("first Begin err = %v", first)
	}
	if p.calls.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("calls = processor %d responder %d", p.calls.Load(), r.calls.Load())
	}
}

func TestBeginDifferentConversationsProceedIndependently(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	if _, err := c.Begin(context.Background(), coordinatorState(t, "conversation-a", "turn-a"), "turn-a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Begin(context.Background(), coordinatorState(t, "conversation-b", "turn-b"), "turn-b", nil); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 || r.calls.Load() != 2 {
		t.Fatalf("calls = processor %d responder %d", p.calls.Load(), r.calls.Load())
	}
}
func TestBeginProcessorErrorFailsReservationAndDoesNotSend(t *testing.T) {
	p := &processorSpy{err: errProcessor}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	_, err := c.Begin(context.Background(), state, "turn-1", nil)
	if !errors.Is(err, errProcessor) {
		t.Fatalf("err = %v", err)
	}
	if r.calls.Load() != 0 {
		t.Fatal("responder called after processor error")
	}
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err == nil {
		t.Fatal("failed reservation was not terminal")
	}
}

func TestBeginInvalidDirectiveFailsReservation(t *testing.T) {
	p := &processorSpy{directive: conversation.TurnDirective{}}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err == nil {
		t.Fatal("invalid directive succeeded")
	}
	if r.calls.Load() != 0 {
		t.Fatal("responder called for invalid directive")
	}
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err == nil {
		t.Fatal("invalid directive left reusable reservation")
	}
}

func TestBeginResponderErrorFailsWithoutRetry(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{err: errResponder}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	_, err := c.Begin(context.Background(), state, "turn-1", nil)
	if !errors.Is(err, errResponder) {
		t.Fatalf("err = %v", err)
	}
	if r.calls.Load() != 1 {
		t.Fatalf("responder calls = %d", r.calls.Load())
	}
}

func TestBeginCanceledBeforeReserveDoesNothing(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Begin(ctx, state, "turn-1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if p.calls.Load() != 0 || r.calls.Load() != 0 {
		t.Fatal("canceled Begin performed work")
	}
}

func TestBeginCanceledAfterReserveFails(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	p := &processorSpy{directive: validDirective(), entered: entered, release: release}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.Begin(ctx, state, "turn-1", nil); result <- err }()
	<-entered
	cancel()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if r.calls.Load() != 0 {
		t.Fatal("responder called after cancellation")
	}
	if _, err := c.Begin(context.Background(), state, "turn-1", nil); err == nil {
		t.Fatal("canceled cycle was not terminal")
	}
}

func TestBeginSendSuccessDoesNotCompleteAndFailCallbackWorks(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	state := coordinatorState(t, "conversation-1", "turn-1")
	key, err := c.Begin(context.Background(), state, "turn-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Fail(key); err != nil {
		t.Fatal(err)
	}
	if err := c.Fail(key); !errors.Is(err, conversation.ErrResponseAlreadyFailed) {
		t.Fatalf("duplicate Fail err = %v", err)
	}
}

func TestCompleteAndFailConcurrentHaveOneTerminalWinner(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	c := newCoordinator(t, p, r)
	key, err := c.Begin(context.Background(), coordinatorState(t, "conversation-1", "turn-1"), "turn-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { results <- c.Complete(key) }()
	go func() { results <- c.Fail(key) }()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("terminal results = %v, %v; expected one success", first, second)
	}
}

func TestCoordinatorRejectsNilDependenciesAndInputs(t *testing.T) {
	p := &processorSpy{directive: validDirective()}
	r := &responderSpy{}
	if _, err := New(nil, r, conversation.NewResponseGate()); !errors.Is(err, ErrInvalidCoordinator) {
		t.Fatalf("nil processor err = %v", err)
	}
	if _, err := New(p, nil, conversation.NewResponseGate()); !errors.Is(err, ErrInvalidCoordinator) {
		t.Fatalf("nil responder err = %v", err)
	}
	if _, err := New(p, r, nil); !errors.Is(err, ErrInvalidCoordinator) {
		t.Fatalf("nil gate err = %v", err)
	}
	c := newCoordinator(t, p, r)
	if _, err := c.Begin(nil, coordinatorState(t, "c", "t"), "t", nil); !errors.Is(err, ErrInvalidCoordinatorInput) {
		t.Fatalf("nil ctx err = %v", err)
	}
	if _, err := c.Begin(context.Background(), nil, "t", nil); !errors.Is(err, ErrInvalidCoordinatorInput) {
		t.Fatalf("nil state err = %v", err)
	}
	if _, err := c.Begin(context.Background(), coordinatorState(t, "c", "t"), "", nil); !errors.Is(err, ErrInvalidCoordinatorInput) {
		t.Fatalf("empty source err = %v", err)
	}
}
