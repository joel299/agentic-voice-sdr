package bridge

import (
	"errors"
	"sync"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

// ResponseDiagnostics is call scoped and contains no payload, transcript,
// endpoint, resumption handle, or provider error text. Counts are not RTP proof.
type ResponseDiagnostics struct {
	AudioEvents            uint64                         `json:"gemini_audio_events"`
	AudioBytes             uint64                         `json:"gemini_audio_bytes"`
	OutputTranscriptEvents uint64                         `json:"gemini_output_transcription_events"`
	GenerationComplete     bool                           `json:"generation_complete_seen"`
	TurnComplete           bool                           `json:"turn_complete_seen"`
	GoAway                 bool                           `json:"go_away_seen"`
	GoAwayTimeLeftMS       int64                          `json:"go_away_time_left_ms"`
	ResumptionUpdate       bool                           `json:"session_resumption_update_seen"`
	Resumable              bool                           `json:"session_resumable"`
	Closed                 bool                           `json:"event_closed_seen"`
	CloseClass             geminilive.CloseStatusClass    `json:"provider_close_class"`
	TransportClass         geminilive.TransportErrorClass `json:"transport_class"`
	LastProviderEvent      geminilive.EventKind           `json:"provider_event_before_failure"`
	LastSuccessfulStage    string                         `json:"last_successful_ai_stage"`
	LastAudioAt            *time.Time                     `json:"last_gemini_audio_at,omitempty"`
	FailureStage           string                         `json:"failure_stage,omitempty"`
	FailureClass           string                         `json:"failure_class,omitempty"`
}

type responseDiagnostics struct {
	mu       sync.Mutex
	snapshot ResponseDiagnostics
}

func (b *SplitBridge) Diagnostics() ResponseDiagnostics {
	b.diagnostics.mu.Lock()
	defer b.diagnostics.mu.Unlock()
	d := b.diagnostics.snapshot
	if d.LastAudioAt != nil {
		at := *d.LastAudioAt
		d.LastAudioAt = &at
	}
	return d
}
func (b *SplitBridge) recordEvent(e geminilive.Event) {
	b.diagnostics.mu.Lock()
	defer b.diagnostics.mu.Unlock()
	d := &b.diagnostics.snapshot
	switch e.Kind {
	case geminilive.EventAudio:
		d.AudioEvents++
		d.AudioBytes += uint64(len(e.Audio))
		at := time.Now().UTC()
		d.LastAudioAt = &at
	case geminilive.EventOutputTranscription:
		d.OutputTranscriptEvents++
	case geminilive.EventGenerationComplete:
		d.GenerationComplete = true
	case geminilive.EventTurnComplete:
		d.TurnComplete = true
	case geminilive.EventGoAway:
		d.GoAway = true
		d.GoAwayTimeLeftMS = e.GoAwayTimeLeftMS
	case geminilive.EventSessionResumption:
		d.ResumptionUpdate = true
		d.Resumable = e.SessionResumable
	case geminilive.EventClosed:
		d.Closed = true
		d.CloseClass = safeCloseStatus(e.CloseStatusClass)
		d.TransportClass = safeTransportClass(e.TransportClass)
	}
	if e.TurnComplete {
		d.TurnComplete = true
	}
	d.LastProviderEvent = e.Kind
}
func (b *SplitBridge) recordFailure(stage string, err error) {
	b.diagnostics.mu.Lock()
	defer b.diagnostics.mu.Unlock()
	d := &b.diagnostics.snapshot
	d.FailureStage = stage
	switch {
	case errors.Is(err, conversation.ErrResponseCycleActive):
		d.FailureClass = "response_cycle_active"
	case errors.Is(err, ErrResponseTurnIncomplete):
		d.FailureClass = "provider_turn_incomplete"
	case errors.Is(err, ErrSessionClosed):
		d.FailureClass = "provider_closed"
	default:
		d.FailureClass = "runtime_error"
	}
	var pe *geminilive.Error
	if errors.As(err, &pe) {
		d.FailureClass = "provider_transport"
		if pe.CloseStatusClass != "" {
			d.CloseClass = safeCloseStatus(pe.CloseStatusClass)
		}
		d.TransportClass = safeTransportClass(pe.TransportClass)
	}
}
func safeTransportClass(c geminilive.TransportErrorClass) geminilive.TransportErrorClass {
	switch c {
	case geminilive.TransportContextCancel, geminilive.TransportContextDeadline, geminilive.TransportRemoteClose, geminilive.TransportIOEOF, geminilive.TransportUnexpectedEOF, geminilive.TransportNetworkTimeout, geminilive.TransportConnectionReset, geminilive.TransportTLS, geminilive.TransportOther:
		return c
	default:
		return geminilive.TransportOther
	}
}
