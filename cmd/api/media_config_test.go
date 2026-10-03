package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
)

// Gemini setup precedes the bridge consumer in the real API. A short early
// utterance must survive the bounded queue until that consumer starts.
func TestLocalMediaRetainsEarlyUtteranceBeforeConsumerStarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := localBaresipMediaConfig()
	if cfg.BufferFrames != 0 || cfg.RXBufferFrames != 0 || cfg.TXSocketBufferBytes != 0 {
		t.Fatal("local API must preserve the baseline bounded media defaults")
	}
	a, err := baresipmedia.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rxPath, txPath := a.SocketPaths()
	rx, err := net.Dial("unix", rxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rx.Close()
	tx, err := net.Dial("unix", txPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	s, err := a.WaitSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const frames = 27 // ~540ms: exactly the reproduced startup regression.
	for i := 0; i < frames; i++ {
		wire, err := audiosocket.Encode(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: []byte{byte(i), 0}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rx.Write(wire); err != nil {
			t.Fatal(err)
		}
	}
	for s.Metrics().RXQueueHighWater < frames {
		select {
		case <-ctx.Done():
			t.Fatal("startup audio was discarded before consumption")
		case <-time.After(time.Millisecond):
		}
	}
	if s.Metrics().RXFramesDropped != 0 {
		t.Fatal("early utterance lost samples")
	}
	for i := 0; i < frames; i++ {
		frame, err := s.ReadFrameContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(frame.Payload) != 2 || frame.Payload[0] != byte(i) {
			t.Fatalf("frame %d reordered or discarded", i)
		}
	}
}
