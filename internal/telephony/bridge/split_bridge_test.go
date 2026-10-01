package bridge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

type splitAudio struct {
	mu     sync.Mutex
	frames []audiosocket.Frame
	writes []audiosocket.Frame
	closed chan struct{}
	once   sync.Once
}

func (a *splitAudio) ReadFrame() (audiosocket.Frame, error) {
	a.mu.Lock()
	if len(a.frames) > 0 {
		f := a.frames[0]
		a.frames = a.frames[1:]
		a.mu.Unlock()
		return f, nil
	}
	a.mu.Unlock()
	return audiosocket.Frame{}, io.EOF
}
func (a *splitAudio) WriteFrame(f audiosocket.Frame) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes = append(a.writes, f)
	return nil
}
func (a *splitAudio) Close() error { a.once.Do(func() { close(a.closed) }); return nil }

type splitInput struct {
	mu       sync.Mutex
	sent     [][]byte
	events   chan geminilive.TranscriptEvent
	receives int
	closed   chan struct{}
	once     sync.Once
}

func (s *splitInput) SendAudio(_ context.Context, p []byte) error {
	s.mu.Lock()
	s.sent = append(s.sent, append([]byte(nil), p...))
	s.mu.Unlock()
	return nil
}
func (s *splitInput) EndAudio(context.Context) error { return nil }
func (s *splitInput) Receive(ctx context.Context) (geminilive.TranscriptEvent, error) {
	s.mu.Lock()
	s.receives++
	s.mu.Unlock()
	select {
	case e := <-s.events:
		return e, nil
	case <-ctx.Done():
		return geminilive.TranscriptEvent{}, ctx.Err()
	}
}
func (s *splitInput) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

type splitResponse struct {
	events   chan geminilive.Event
	receives int
	mu       sync.Mutex
	sent     []string
	closed   chan struct{}
	once     sync.Once
}

func (s *splitResponse) SendControlledTurn(_ context.Context, text string, _ conversation.TurnDirective) error {
	s.mu.Lock()
	s.sent = append(s.sent, text)
	s.mu.Unlock()
	return nil
}
func (s *splitResponse) Receive(ctx context.Context) (geminilive.Event, error) {
	s.mu.Lock()
	s.receives++
	s.mu.Unlock()
	select {
	case e := <-s.events:
		return e, nil
	case <-ctx.Done():
		return geminilive.Event{}, ctx.Err()
	}
}
func (s *splitResponse) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

type splitTranscriptHandler struct {
	mu     sync.Mutex
	events []geminilive.TranscriptEvent
	finals chan string
}

func (h *splitTranscriptHandler) HandleTranscript(_ context.Context, e geminilive.TranscriptEvent) error {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	if e.State == geminilive.TranscriptFinal {
		h.finals <- e.Text
	}
	return nil
}

type splitLifecycle struct {
	mu             sync.Mutex
	authorized     bool
	complete, fail int
}
type splitLease struct{ owner *splitLifecycle }

func (l *splitLifecycle) CaptureActive() ResponseTurnLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.authorized {
		return nil
	}
	return &splitLease{owner: l}
}
func (l *splitLifecycle) FailActive(context.Context, error) error { return nil }
func (l *splitLease) ModelAudioAuthorized() bool                  { return true }
func (l *splitLease) Complete(context.Context) error {
	l.owner.mu.Lock()
	l.owner.complete++
	l.owner.mu.Unlock()
	return nil
}
func (l *splitLease) Fail(context.Context, error) error {
	l.owner.mu.Lock()
	l.owner.fail++
	l.owner.mu.Unlock()
	return nil
}

func TestSplitBridgeConnectsRealSessionBoundaries(t *testing.T) {
	input := &splitInput{events: make(chan geminilive.TranscriptEvent, 2), closed: make(chan struct{})}
	response := &splitResponse{events: make(chan geminilive.Event, 4), closed: make(chan struct{})}
	audio := &splitAudio{frames: []audiosocket.Frame{{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2, 3}}}, closed: make(chan struct{})}
	handler := &splitTranscriptHandler{finals: make(chan string, 1)}
	lifecycle := &splitLifecycle{authorized: true}
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptInterim, Text: " partial "}
	input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: " final lead "}
	done := make(chan error, 1)
	go func() {
		done <- NewSplit(audio, audio, input, response, handler, nil, lifecycle).Run(context.Background())
	}()
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{9, 8}, AudioMimeType: "audio/pcm;rate=24000"}
	response.events <- geminilive.Event{Kind: geminilive.EventGenerationComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(input.sent) != 1 || string(input.sent[0]) != "\x01\x02\x03" {
		t.Fatalf("input PCM=%v", input.sent)
	}
	if len(response.sent) != 0 {
		t.Fatalf("fake response unexpectedly received turn; runtime boundary must not synthesize sends: %v", response.sent)
	}
	if input.receives < 1 {
		t.Fatalf("transcriber Receive owners=%d, was not called", input.receives)
	}
	if response.receives < 1 {
		t.Fatalf("response Receive owners=%d, was not called", response.receives)
	}
	if len(audio.writes) != 1 || string(audio.writes[0].Payload) != "\x09\x08" {
		t.Fatalf("output=%v", audio.writes)
	}
	if lifecycle.complete != 1 {
		t.Fatalf("complete=%d", lifecycle.complete)
	}
}

func TestSplitBridgeNeverAcceptsResponsePCM(t *testing.T) {
	input := &splitInput{events: make(chan geminilive.TranscriptEvent, 1), closed: make(chan struct{})}
	response := &splitResponse{events: make(chan geminilive.Event, 1), closed: make(chan struct{})}
	audio := &splitAudio{frames: nil, closed: make(chan struct{})}
	h := &splitTranscriptHandler{finals: make(chan string, 1)}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	if err := NewSplit(audio, audio, input, response, h, nil, &splitLifecycle{}).Run(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(input.sent) != 0 {
		t.Fatal("unexpected input send")
	}
}

func TestSplitBridgeFailsActiveResponseOnRemoteCloseBeforeTurnComplete(t *testing.T) {
	input := &splitInput{events: make(chan geminilive.TranscriptEvent, 1), closed: make(chan struct{})}
	response := &splitResponse{events: make(chan geminilive.Event, 3), closed: make(chan struct{})}
	audio := &splitAudio{closed: make(chan struct{})}
	lifecycle := &splitLifecycle{authorized: true}
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "partial"}
	response.events <- geminilive.Event{Kind: geminilive.EventGenerationComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed, CloseStatusClass: geminilive.CloseStatusNormal}
	err := NewSplit(audio, audio, input, response, &splitTranscriptHandler{finals: make(chan string, 1)}, nil, lifecycle).Run(context.Background())
	if !errors.Is(err, ErrResponseTurnIncomplete) {
		t.Fatalf("Run error=%v, want ErrResponseTurnIncomplete", err)
	}
	if lifecycle.fail != 1 || lifecycle.complete != 0 {
		t.Fatalf("response lifecycle complete=%d fail=%d", lifecycle.complete, lifecycle.fail)
	}
}
