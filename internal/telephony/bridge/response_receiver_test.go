package bridge

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

type slowOrderedPCM struct{ samples []uint16 }

type tracedLifecycle struct{ *splitLifecycle }
type tracedLease struct{ *splitLease }

func (l *tracedLifecycle) CaptureActive() ResponseTurnLease {
	return &tracedLease{&splitLease{owner: l.splitLifecycle}}
}
func (*tracedLease) ResponseTurnID() string { return "response-test-turn" }

func (s *slowOrderedPCM) WriteFrame(f audiosocket.Frame) error {
	return s.WriteFrameContext(context.Background(), f)
}
func (s *slowOrderedPCM) WriteFrameContext(ctx context.Context, f audiosocket.Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Millisecond):
	}
	s.samples = append(s.samples, binary.LittleEndian.Uint16(f.Payload))
	return nil
}

func TestResponseReceiverPreservesHundredEventsWithSlowWriter(t *testing.T) {
	for _, tracing := range []bool{false, true} {
		name := "without_telemetry"
		if tracing {
			name = "with_telemetry"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			response := &splitResponse{events: make(chan geminilive.Event, 104), closed: make(chan struct{})}
			output := &slowOrderedPCM{}
			lifecycle := &splitLifecycle{authorized: true}
			var kinds []geminilive.EventKind
			b := &SplitBridge{output: output, responder: response, lifecycle: lifecycle, events: func(_ context.Context, e geminilive.Event) error { kinds = append(kinds, e.Kind); return nil }}
			if tracing {
				collector := telemetry.NewTurnCollector(2)
				collector.Begin(ctx, "response-test-turn", time.Now())
				b.SetTurnCollector(collector)
				b.lifecycle = &tracedLifecycle{lifecycle}
			}
			for i := 0; i < 100; i++ {
				response.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{byte(i), 0}, AudioMimeType: "audio/pcm;rate=24000"}
			}
			response.events <- geminilive.Event{Kind: geminilive.EventGenerationComplete}
			response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
			response.events <- geminilive.Event{Kind: geminilive.EventClosed}
			if err := b.runSplitResponses(ctx); err != nil {
				t.Fatal(err)
			}
			if len(output.samples) != 100 || len(kinds) != 103 {
				t.Fatalf("lost PCM/events: %d/%d", len(output.samples), len(kinds))
			}
			for i, sample := range output.samples {
				if sample != uint16(i) || kinds[i] != geminilive.EventAudio {
					t.Fatalf("reordered event %d", i)
				}
			}
			if kinds[100] != geminilive.EventGenerationComplete || kinds[101] != geminilive.EventTurnComplete || kinds[102] != geminilive.EventClosed {
				t.Fatal("lifecycle reordered")
			}
			if lifecycle.complete != 1 || lifecycle.fail != 0 {
				t.Fatal("telemetry/slow writer changed lease lifecycle")
			}
		})
	}
}

func TestResponseReceiverCancellationUnblocksFullChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	response := &splitResponse{events: make(chan geminilive.Event, 16), closed: make(chan struct{})}
	for i := 0; i < 16; i++ {
		response.events <- geminilive.Event{Kind: geminilive.EventAudio}
	}
	receiver := newResponseReceiver(ctx, response)
	defer cancel()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(receiver.events) < cap(receiver.events) {
		select {
		case <-deadline.C:
			t.Fatal("receive owner stalled before filling bounded channel")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-receiver.done:
	case <-time.After(time.Second):
		t.Fatal("canceled receive owner leaked")
	}
}

func TestSplitInterruptedWithoutActiveTelemetryTrace(t *testing.T) {
	for _, tracing := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response := &splitResponse{events: make(chan geminilive.Event, 3), closed: make(chan struct{})}
		b := &SplitBridge{responder: response, output: &splitAudio{closed: make(chan struct{})}}
		if tracing {
			b.SetTurnCollector(telemetry.NewTurnCollector(2))
		}
		response.events <- geminilive.Event{Kind: geminilive.EventInterrupted}
		response.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
		response.events <- geminilive.Event{Kind: geminilive.EventClosed}
		if err := b.runSplitResponses(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}
