package voiceflow

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

type e2eAudio struct {
	mu     sync.Mutex
	frames []audiosocket.Frame
	writes []audiosocket.Frame
}

func (a *e2eAudio) ReadFrame() (audiosocket.Frame, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.frames) == 0 {
		return audiosocket.Frame{}, io.EOF
	}
	f := a.frames[0]
	a.frames = a.frames[1:]
	return f, nil
}
func (a *e2eAudio) WriteFrame(f audiosocket.Frame) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes = append(a.writes, f)
	return nil
}
func (a *e2eAudio) Close() error { return nil }

type e2eInput struct {
	mu     sync.Mutex
	events chan geminilive.TranscriptEvent
	sent   [][]byte
	seen   chan geminilive.TranscriptEvent
}

func (s *e2eInput) SendAudio(_ context.Context, p []byte) error {
	s.mu.Lock()
	s.sent = append(s.sent, append([]byte(nil), p...))
	s.mu.Unlock()
	return nil
}
func (s *e2eInput) EndAudio(context.Context) error { return nil }
func (s *e2eInput) Receive(ctx context.Context) (geminilive.TranscriptEvent, error) {
	select {
	case e := <-s.events:
		s.seen <- e
		return e, nil
	case <-ctx.Done():
		return geminilive.TranscriptEvent{}, ctx.Err()
	}
}
func (s *e2eInput) Close() error { return nil }

type e2eResponse struct {
	mu         sync.Mutex
	sendCalls  int
	finalText  string
	directive  conversation.TurnDirective
	sendCalled chan struct{}
	events     chan geminilive.Event
}

func (s *e2eResponse) SendControlledTurn(_ context.Context, text string, d conversation.TurnDirective) error {
	s.mu.Lock()
	s.sendCalls++
	s.finalText = text
	s.directive = d
	s.mu.Unlock()
	close(s.sendCalled)
	return nil
}
func (s *e2eResponse) Receive(ctx context.Context) (geminilive.Event, error) {
	select {
	case e := <-s.events:
		return e, nil
	case <-ctx.Done():
		return geminilive.Event{}, ctx.Err()
	}
}
func (s *e2eResponse) Close() error { return nil }

type e2eProcessor struct {
	mu    sync.Mutex
	calls int
}

func (p *e2eProcessor) ProcessTurn(context.Context, turnruntime.TurnInput) (conversation.TurnDirective, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}, nil
}

func TestNewSplitRuntimeEndToEndUsesOneControlledSession(t *testing.T) {
	state, err := conversation.NewConversationState("e2e-split")
	if err != nil {
		t.Fatal(err)
	}
	processor := &e2eProcessor{}
	response := &e2eResponse{sendCalled: make(chan struct{}), events: make(chan geminilive.Event, 3)}
	input := &e2eInput{events: make(chan geminilive.TranscriptEvent, 2), seen: make(chan geminilive.TranscriptEvent, 2)}
	audio := &e2eAudio{frames: []audiosocket.Frame{{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2}}}}
	gate := conversation.NewResponseGate()
	repo := &transcriptRepoFake{}
	runtime, err := NewSplitRuntimeWithTranscriptPersistence(audio, audio, input, response, state, processor, gate, nil, nil, "call_X", repo)
	if err != nil {
		t.Fatal(err)
	}
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptInterim, Text: " partial "}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(context.Background()) }()
	<-input.seen
	response.mu.Lock()
	if response.sendCalls != 0 {
		t.Fatalf("interim SendControlledTurn calls=%d", response.sendCalls)
	}
	response.mu.Unlock()
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "  final lead  ", EventID: "receive-1"}
	<-response.sendCalled
	response.mu.Lock()
	if response.sendCalls != 1 || response.finalText != "final lead" {
		t.Fatalf("controlled send calls=%d text=%q", response.sendCalls, response.finalText)
	}
	if response.directive.Kind != conversation.ActionAskQuestion || response.directive.Reason != conversation.ReasonNeedsClarification {
		t.Fatalf("controlled directive=%#v", response.directive)
	}
	response.mu.Unlock()
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Sure,", EventID: "receive-1"}
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: " here is", EventID: "receive-2"}
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: " the answer.", EventID: "receive-3"}
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{9, 8}, AudioMimeType: "audio/pcm;rate=24000"}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	turns := state.Turns()
	if len(turns) != 1 || turns[0].ID != "lead-000001" || turns[0].Text != "final lead" || turns[0].Transcript != conversation.TranscriptFinal {
		t.Fatalf("turns=%#v", turns)
	}
	processor.mu.Lock()
	if processor.calls != 1 {
		t.Fatalf("Coordinator.Begin processor calls=%d", processor.calls)
	}
	processor.mu.Unlock()
	if len(audio.writes) != 1 || audio.writes[0].Type != audiosocket.TypeSlin24 || string(audio.writes[0].Payload) != "\x09\x08" {
		t.Fatalf("output=%#v", audio.writes)
	}
	if len(repo.turns) != 2 || repo.turns[0].CallID != "call_X" || repo.turns[0].Role != "lead" || repo.turns[1].CallID != "call_X" || repo.turns[1].Role != "agent" || repo.turns[1].Text != "Sure, here is the answer." {
		t.Fatalf("transcript call id or aggregated agent output is wrong: %+v", repo.turns)
	}
	if len(input.sent) != 1 || string(input.sent[0]) != "\x01\x02" {
		t.Fatalf("input PCM=%#v", input.sent)
	}
	key := conversation.ResponseKey{ConversationID: "e2e-split", SourceTurnID: "lead-000001"}
	if err := gate.Complete(key); !errors.Is(err, conversation.ErrResponseAlreadyCompleted) {
		t.Fatalf("exact response key completion read-back error=%v", err)
	}
}

func TestProductionInputBoundaryDeduplicatesReplayedFinalTurn(t *testing.T) {
	state, err := conversation.NewConversationState("call_X")
	if err != nil {
		t.Fatal(err)
	}
	processor := &e2eProcessor{}
	response := &e2eResponse{sendCalled: make(chan struct{}), events: make(chan geminilive.Event, 4)}
	input := &e2eInput{events: make(chan geminilive.TranscriptEvent, 4), seen: make(chan geminilive.TranscriptEvent, 4)}
	repo := &transcriptRepoFake{}
	runtime, err := NewSplitRuntimeWithTranscriptPersistence(&e2eAudio{}, &e2eAudio{}, input, response, state, processor, conversation.NewResponseGate(), nil, nil, "call_X", repo)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(context.Background()) }()

	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptInterim, Text: "Sim", EventID: "receive-1"}
	<-input.seen
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Sim", EventID: "receive-1"}
	<-response.sendCalled
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Sim", EventID: "receive-1"}
	<-input.seen
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Entendi."}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	turns := state.Turns()
	if len(turns) != 1 || turns[0].ID != "lead-000001" || turns[0].Text != "Sim" {
		t.Fatalf("conversation lead turns after replay: %+v", turns)
	}
	leadRows := 0
	for _, turn := range repo.turns {
		if turn.Role == "lead" {
			leadRows++
			if turn.IdempotencyKey != "lead:call_X:lead-000001" {
				t.Fatalf("lead business identity depends on provider event: %+v", turn)
			}
		}
	}
	if leadRows != 1 || processor.calls != 1 {
		t.Fatalf("lead DB rows=%d, JEV/TurnRuntime begins=%d; want 1 each; rows=%+v", leadRows, processor.calls, repo.turns)
	}
}

func TestBaresipTranscriptRuntimeUsesCanonicalAPICallID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	media, err := baresipmedia.New(ctx, baresipmedia.Config{ParentDir: t.TempDir(), BufferFrames: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	rxPath, txPath := media.SocketPaths()
	dialer := net.Dialer{}
	rxPeer, err := dialer.DialContext(ctx, "unix", rxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rxPeer.Close()
	txPeer, err := dialer.DialContext(ctx, "unix", txPath)
	if err != nil {
		t.Fatal(err)
	}
	defer txPeer.Close()
	session, err := media.WaitSession(ctx)
	if err != nil {
		t.Fatal(err)
	}

	state, err := conversation.NewConversationState("call_X")
	if err != nil {
		t.Fatal(err)
	}
	repo := &transcriptRepoFake{}
	input := &e2eInput{events: make(chan geminilive.TranscriptEvent, 1), seen: make(chan geminilive.TranscriptEvent, 1)}
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "lead final"}
	response := &e2eResponse{sendCalled: make(chan struct{}), events: make(chan geminilive.Event, 4)}
	runtime, err := NewBaresipSplitRuntimeWithTranscriptPersistence(session, input, response, state, &e2eProcessor{}, conversation.NewResponseGate(), nil, nil, "call_X", repo)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-input.seen:
	case <-time.After(time.Second):
		t.Fatal("input transcript was not received")
	}
	select {
	case <-response.sendCalled:
	case <-time.After(time.Second):
		t.Fatal("authorized response was not started")
	}
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "agent final"}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Baresip media runtime did not stop")
	}
	if len(repo.turns) != 2 || repo.turns[0].CallID != "call_X" || repo.turns[0].Role != "lead" || repo.turns[1].CallID != "call_X" || repo.turns[1].Role != "agent" {
		t.Fatalf("API call ID was not preserved across Baresip media/Gemini: %+v", repo.turns)
	}
}
