package telemetry

import (
	"context"
	"sync"
	"time"
)

// TurnSnapshot accepts metadata only. Missing clocks stay missing, including
// speech-end, C emission and RTP when those boundaries were not observed.
type TurnSnapshot struct {
	TurnID                string                `json:"turn_id"`
	SpeechEndBasis        string                `json:"speech_end_basis,omitempty"`
	Times                 map[string]time.Time  `json:"times"`
	JEVNetwork            *RequestNetworkTiming `json:"jev_network,omitempty"`
	GeminiFirstAudioBytes int                   `json:"gemini_first_audio_bytes,omitempty"`
}
type TurnTrace struct {
	mu       sync.Mutex
	snapshot TurnSnapshot
}
type TurnCollector struct {
	mu       sync.Mutex
	traces   []*TurnTrace
	capacity int
	dropped  uint64
}
type turnTraceKey struct{}

func NewTurnCollector(capacity int) *TurnCollector {
	if capacity < 1 {
		capacity = 1
	}
	if capacity > 256 {
		capacity = 256
	}
	return &TurnCollector{capacity: capacity}
}
func (c *TurnCollector) Begin(ctx context.Context, id string, at time.Time) (context.Context, *TurnTrace) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.traces {
		if existing.snapshot.TurnID == id {
			return WithTurnTrace(ctx, existing), existing
		}
	}
	t := &TurnTrace{snapshot: TurnSnapshot{TurnID: id, Times: make(map[string]time.Time)}}
	t.Mark("lead_final_transcript_at", at)
	if len(c.traces) < c.capacity {
		c.traces = append(c.traces, t)
	} else {
		c.dropped++
	}
	return context.WithValue(ctx, turnTraceKey{}, t), t
}
func MarkTurn(ctx context.Context, key string) {
	if ctx == nil {
		return
	}
	if t, ok := ctx.Value(turnTraceKey{}).(*TurnTrace); ok {
		t.Mark(key, time.Now())
	}
}
func (t *TurnTrace) Mark(key string, at time.Time) {
	if t == nil || !turnTimestampAllowed(key) || at.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.snapshot.Times[key]; !ok {
		t.snapshot.Times[key] = at
	}
}
func (t *TurnTrace) Snapshot() TurnSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := TurnSnapshot{TurnID: t.snapshot.TurnID, SpeechEndBasis: t.snapshot.SpeechEndBasis, GeminiFirstAudioBytes: t.snapshot.GeminiFirstAudioBytes, Times: map[string]time.Time{}}
	if t.snapshot.JEVNetwork != nil {
		v := *t.snapshot.JEVNetwork
		s.JEVNetwork = &v
	}
	for k, v := range t.snapshot.Times {
		s.Times[k] = v
	}
	return s
}
func (c *TurnCollector) Find(id string) *TurnTrace {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.traces {
		if t.snapshot.TurnID == id {
			return t
		}
	}
	return nil
}
func (c *TurnCollector) Snapshot() []TurnSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]TurnSnapshot, 0, len(c.traces))
	for _, t := range c.traces {
		out = append(out, t.Snapshot())
	}
	return out
}
func (c *TurnCollector) Dropped() uint64 { c.mu.Lock(); defer c.mu.Unlock(); return c.dropped }
func turnTimestampAllowed(k string) bool {
	switch k {
	case "lead_speech_end_at", "lead_final_transcript_at", "jev_started_at", "jev_completed_at", "turn_directive_sent_at", "gemini_response_request_at", "gemini_first_output_transcription_at", "gemini_first_audio_at", "gemini_generation_complete_at", "gemini_turn_complete_at", "go_first_pcm_write_at", "c_source_first_real_frame_at", "c_first_real_frame_received_at", "first_outbound_rtp_at", "barge_in_started_at", "barge_in_cleared_at":
		return true
	}
	return false
}
func (s TurnSnapshot) Metrics() map[string]float64 {
	out := map[string]float64{}
	for _, p := range [][3]string{
		{"speech_end_to_final_transcript_ms", "lead_speech_end_at", "lead_final_transcript_at"},
		{"jev_latency_ms", "jev_started_at", "jev_completed_at"},
		{"final_transcript_to_jev_start_ms", "lead_final_transcript_at", "jev_started_at"},
		{"jev_complete_to_turn_directive_ms", "jev_completed_at", "turn_directive_sent_at"},
		{"directive_to_first_gemini_audio_ms", "turn_directive_sent_at", "gemini_first_audio_at"},
		{"turn_directive_to_send_ms", "turn_directive_sent_at", "gemini_response_request_at"},
		{"send_to_first_output_transcription_ms", "gemini_response_request_at", "gemini_first_output_transcription_at"},
		{"send_to_first_audio_ms", "gemini_response_request_at", "gemini_first_audio_at"},
		{"gemini_to_go_media_ms", "gemini_first_audio_at", "go_first_pcm_write_at"},
		{"go_to_c_source_ms", "go_first_pcm_write_at", "c_source_first_real_frame_at"},
		{"go_write_to_c_receive_ms", "go_first_pcm_write_at", "c_first_real_frame_received_at"},
		{"c_receive_to_c_emit_ms", "c_first_real_frame_received_at", "c_source_first_real_frame_at"},
		{"speech_end_to_first_agent_media_ms", "lead_speech_end_at", "c_source_first_real_frame_at"},
		{"final_transcript_to_first_go_pcm_ms", "lead_final_transcript_at", "go_first_pcm_write_at"},
		{"barge_in_cancel_latency_ms", "barge_in_started_at", "barge_in_cleared_at"},
	} {
		a, ok := s.Times[p[1]]
		b, ok2 := s.Times[p[2]]
		if ok && ok2 && !b.Before(a) {
			name := p[0]
			if p[1] == "lead_speech_end_at" && s.SpeechEndBasis != "fixture_known_end" {
				name = "estimated_" + name
			}
			out[name] = float64(b.Sub(a)) / float64(time.Millisecond)
		}
	}
	return out
}

func TraceFromContext(ctx context.Context) *TurnTrace {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(turnTraceKey{}).(*TurnTrace)
	return t
}
func WithTurnTrace(ctx context.Context, t *TurnTrace) context.Context {
	return context.WithValue(ctx, turnTraceKey{}, t)
}

func (t *TurnTrace) SetSpeechEndBasis(b string) {
	if t == nil || (b != "fixture_known_end" && b != "pcm_energy_estimate") {
		return
	}
	t.mu.Lock()
	if t.snapshot.SpeechEndBasis == "" {
		t.snapshot.SpeechEndBasis = b
	}
	t.mu.Unlock()
}

// RequestNetworkTiming excludes provider content, URLs and credentials.
type RequestNetworkTiming struct {
	RequestBytes     int     `json:"request_bytes"`
	ConnectionReused bool    `json:"connection_reused"`
	DNSMS            float64 `json:"dns_ms"`
	ConnectMS        float64 `json:"connect_ms"`
	TLSMS            float64 `json:"tls_ms"`
	FirstByteMS      float64 `json:"first_byte_ms"`
	TotalMS          float64 `json:"total_ms"`
}

func (t *TurnTrace) SetJEVNetwork(v RequestNetworkTiming) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snapshot.JEVNetwork == nil {
		t.snapshot.JEVNetwork = &v
	}
}

func (t *TurnTrace) SetFirstAudioBytes(n int) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.snapshot.GeminiFirstAudioBytes == 0 {
		t.snapshot.GeminiFirstAudioBytes = n
	}
}
