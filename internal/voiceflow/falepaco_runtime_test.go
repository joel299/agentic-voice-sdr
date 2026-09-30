package voiceflow

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

type runtimeTestInput struct{ once sync.Once }

func (s *runtimeTestInput) SendAudio(context.Context, []byte) error { return nil }
func (s *runtimeTestInput) EndAudio(context.Context) error          { return nil }
func (s *runtimeTestInput) Receive(ctx context.Context) (geminilive.TranscriptEvent, error) {
	sent := false
	s.once.Do(func() { sent = true })
	if sent {
		return geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "  hello from Paco  ", EventID: "lead-event"}, nil
	}
	<-ctx.Done()
	return geminilive.TranscriptEvent{}, ctx.Err()
}
func (s *runtimeTestInput) Close() error { return nil }

type runtimeTestResponse struct {
	mu     sync.Mutex
	sends  int
	text   string
	events []geminilive.Event
	ready  chan struct{}
	once   sync.Once
}

func (s *runtimeTestResponse) SendControlledTurn(_ context.Context, text string, _ conversation.TurnDirective) error {
	s.mu.Lock()
	s.sends++
	s.text = text
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
	return nil
}
func (s *runtimeTestResponse) Receive(ctx context.Context) (geminilive.Event, error) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return geminilive.Event{}, ctx.Err()
	}
	s.mu.Lock()
	if len(s.events) > 0 {
		e := s.events[0]
		s.events = s.events[1:]
		s.mu.Unlock()
		return e, nil
	}
	s.mu.Unlock()
	<-ctx.Done()
	return geminilive.Event{}, ctx.Err()
}
func (s *runtimeTestResponse) Close() error { return nil }

type runtimeTestProcessor struct{}

func (runtimeTestProcessor) ProcessTurn(context.Context, turnruntime.TurnInput) (conversation.TurnDirective, error) {
	return conversation.TurnDirective{Kind: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}, nil
}

func TestFalePacoRuntimeUsesLegacyAudioSocketComposition(t *testing.T) {
	response := &runtimeTestResponse{ready: make(chan struct{}), events: []geminilive.Event{{Kind: geminilive.EventOutputTranscription, Text: "Agent reply", EventID: "agent-event"}, {Kind: geminilive.EventAudio, AudioMimeType: "audio/pcm;rate=24000", Audio: []byte{1, 2}}, {Kind: geminilive.EventTurnComplete}}}
	var gotID string
	runtime, err := NewFalePacoRuntime(FalePacoRuntimeConfig{AudioSocketAddr: "127.0.0.1:0", Processor: runtimeTestProcessor{}, Sessions: func(_ context.Context, id string) (geminilive.InputTranscriberSession, geminilive.ControlledResponseSession, error) {
		gotID = id
		return &runtimeTestInput{}, response, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	deadline := time.Now().Add(time.Second)
	for runtime.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.Addr() == "" {
		t.Fatal("runtime did not listen")
	}
	badConn, err := net.Dial("tcp", runtime.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = badConn.Close()
	conn, err := net.Dial("tcp", runtime.Addr())
	if err != nil {
		t.Fatal(err)
	}
	stream := audiosocket.NewStream(conn, conn)
	id := make([]byte, 16)
	for i := range id {
		id[i] = byte(i)
	}
	if err = stream.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeID, Payload: id}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := stream.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != audiosocket.TypeSlin24 || string(frame.Payload) != string([]byte{1, 2}) {
		t.Fatalf("unexpected output: %#v", frame)
	}
	if response.sends != 1 || response.text != "hello from Paco" {
		t.Fatalf("send=%d text=%q", response.sends, response.text)
	}
	if gotID != "call-000102030405060708090a0b0c0d0e0f" {
		t.Fatalf("unexpected sanitized session id: %q", gotID)
	}
	_ = conn.Close()
	_ = runtime.Shutdown(context.Background())
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
}
func TestSanitizeFalePacoCallIDNeverReturnsRawPayload(t *testing.T) {
	got := sanitizeFalePacoCallID([]byte("attacker\r\nvalue"))
	if got == "attacker\r\nvalue" || got == "" {
		t.Fatalf("unsafe id: %q", got)
	}
}
