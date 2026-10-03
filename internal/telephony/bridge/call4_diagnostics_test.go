package bridge

import (
	"context"
	"errors"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"testing"
	"time"
)

type unavailableOutput struct{}

func (unavailableOutput) WriteFrame(audiosocket.Frame) error {
	return errors.New("local source unavailable")
}

func TestCall4IntroGenerationThenGoAwayCloseIsProviderBoundary(t *testing.T) {
	response := &splitResponse{events: make(chan geminilive.Event, 5)}
	response.events <- geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Sou o Joel"}
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: make([]byte, 960), AudioMimeType: "audio/pcm;rate=24000"}
	response.events <- geminilive.Event{Kind: geminilive.EventGenerationComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventGoAway, GoAwayTimeLeftMS: 1000}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed, CloseStatusClass: geminilive.CloseStatusGoingAway, TransportClass: geminilive.TransportRemoteClose}
	b := &SplitBridge{responder: response, output: &splitAudio{}, lifecycle: &splitLifecycle{authorized: true}}
	err := b.runSplitResponses(context.Background())
	var staged *StageError
	if !errors.Is(err, ErrResponseTurnIncomplete) || !errors.As(err, &staged) || staged.Stage != "gemini_response_receive" {
		t.Fatalf("error=%v", err)
	}
	d := b.Diagnostics()
	if d.AudioEvents != 1 || d.AudioBytes != 960 || !d.GoAway || d.GoAwayTimeLeftMS != 1000 || !d.Closed || !d.GenerationComplete || d.TurnComplete || d.TransportClass != geminilive.TransportRemoteClose {
		t.Fatalf("diagnostics=%+v", d)
	}
}
func TestCall4AudioSourceUnavailableIsMediaBoundary(t *testing.T) {
	response := &splitResponse{events: make(chan geminilive.Event, 2)}
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: make([]byte, 960), AudioMimeType: "audio/pcm;rate=24000"}
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: make([]byte, 960), AudioMimeType: "audio/pcm;rate=24000"}
	b := &SplitBridge{responder: response, output: unavailableOutput{}, lifecycle: &splitLifecycle{authorized: true}}
	err := b.runSplitResponses(context.Background())
	var staged *StageError
	if !errors.As(err, &staged) || staged.Stage != "media_egress" {
		t.Fatalf("error=%v", err)
	}
	if d := b.Diagnostics(); d.AudioEvents != 1 || d.Closed {
		t.Fatalf("diagnostics=%+v", d)
	}
}

type pressureOutput struct {
	queue   chan audiosocket.Frame
	blocked chan struct{}
}

func (w *pressureOutput) WriteFrame(f audiosocket.Frame) error {
	return w.WriteFrameContext(context.Background(), f)
}
func (w *pressureOutput) WriteFrameContext(ctx context.Context, f audiosocket.Frame) error {
	if len(w.queue) == cap(w.queue) {
		select {
		case w.blocked <- struct{}{}:
		default:
		}
	}
	select {
	case w.queue <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestCall4QueuePressureContinuesAudioUntilTurnComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response := &splitResponse{events: make(chan geminilive.Event, 6)}
	for i := 0; i < 4; i++ {
		response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: make([]byte, 960), AudioMimeType: "audio/pcm;rate=24000"}
	}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	writer := &pressureOutput{queue: make(chan audiosocket.Frame, 1), blocked: make(chan struct{}, 1)}
	lifecycle := &splitLifecycle{authorized: true}
	b := &SplitBridge{responder: response, output: writer, lifecycle: lifecycle}
	done := make(chan error, 1)
	go func() { done <- b.runSplitResponses(ctx) }()
	select {
	case <-writer.blocked:
	case <-ctx.Done():
		t.Fatal("no bounded backpressure")
	}
	for i := 0; i < 4; i++ {
		select {
		case <-writer.queue:
		case <-ctx.Done():
			t.Fatal("audio stopped under pressure")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if d := b.Diagnostics(); d.AudioEvents != 4 || !d.TurnComplete {
		t.Fatalf("diagnostics=%+v", d)
	}
	if lifecycle.complete != 1 || lifecycle.fail != 0 {
		t.Fatalf("lifecycle=%+v", lifecycle)
	}
}
