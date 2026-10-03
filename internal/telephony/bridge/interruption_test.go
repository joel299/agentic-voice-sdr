package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

type interruptibleAudio struct {
	*splitAudio
	entered   chan struct{}
	cancelled chan struct{}
	flushed   chan struct{}
}

func (a *interruptibleAudio) WriteFrameContext(ctx context.Context, f audiosocket.Frame) error {
	close(a.entered)
	<-ctx.Done()
	close(a.cancelled)
	return ctx.Err()
}
func (a *interruptibleAudio) DiscardPendingAudio(context.Context) error { close(a.flushed); return nil }
func TestSplitInterruptedCancelsBlockedWriteAndFlushesPendingAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response := &splitResponse{events: make(chan geminilive.Event, 4), closed: make(chan struct{})}
	a := &interruptibleAudio{splitAudio: &splitAudio{closed: make(chan struct{})}, entered: make(chan struct{}), cancelled: make(chan struct{}), flushed: make(chan struct{})}
	lifecycle := &splitLifecycle{authorized: true}
	b := &SplitBridge{output: a, responder: response, lifecycle: lifecycle}
	done := make(chan error, 1)
	go func() { done <- b.runSplitResponses(ctx) }()
	response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{1, 0, 2}, AudioMimeType: "audio/pcm;rate=24000"}
	select {
	case <-a.entered:
	case <-ctx.Done():
		t.Fatal("write not started")
	}
	response.events <- geminilive.Event{Kind: geminilive.EventInterrupted}
	select {
	case <-a.flushed:
	case <-ctx.Done():
		t.Fatal("blocked PCM prevented interruption")
	}
	response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	response.events <- geminilive.Event{Kind: geminilive.EventClosed}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if lifecycle.fail != 1 || lifecycle.complete != 0 {
		t.Fatalf("lifecycle fail=%d complete=%d", lifecycle.fail, lifecycle.complete)
	}
	select {
	case <-a.cancelled:
	default:
		t.Fatal("PCM write not cancelled")
	}
}
