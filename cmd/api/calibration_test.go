package main

import (
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

func TestGeminiReceiveFailureClassReportsStageProgressWithoutPayload(t *testing.T) {
	got := geminiReceiveFailureClass(&geminilive.Error{Kind: geminilive.ErrorReceive, TransportClass: geminilive.TransportOther}, httpapi.GeminiTurnMetadata{AudioEventCount: 2, AudioBytesTotal: 48000, GenerationComplete: true})
	want := "gemini_response_receive_receive_transport_other_audio_events_2_generation_complete_true_turn_complete_false"
	if got != want {
		t.Fatalf("safe Gemini stage class=%q, want %q", got, want)
	}
}
