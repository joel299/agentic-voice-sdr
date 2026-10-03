package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTurnTimingFirstWinsBoundedAndNoInventedSpeechEnd(t *testing.T) {
	c := NewTurnCollector(2)
	ctx, tr := c.Begin(context.Background(), "lead-000001", time.Now())
	MarkTurn(ctx, "jev_started_at")
	MarkTurn(ctx, "jev_completed_at")
	start := time.Now()
	tr.Mark("gemini_first_audio_at", start)
	tr.Mark("gemini_first_audio_at", start.Add(time.Second))
	if !tr.Snapshot().Times["gemini_first_audio_at"].Equal(start) {
		t.Fatal("first audio overwritten")
	}
	if _, ok := tr.Snapshot().Metrics()["speech_end_to_first_agent_media_ms"]; ok {
		t.Fatal("invented speech end")
	}
	c.Begin(ctx, "lead-000002", start)
	c.Begin(ctx, "lead-000003", start)
	if len(c.Snapshot()) != 2 || c.Dropped() != 1 {
		t.Fatal("unbounded collector")
	}
	b, _ := json.Marshal(c.Snapshot())
	if strings.Contains(string(b), "transcript_text") {
		t.Fatal("content leak")
	}
}
func TestTurnTimingConcurrentStages(t *testing.T) {
	c := NewTurnCollector(1)
	_, tr := c.Begin(context.Background(), "lead-000001", time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); tr.Mark("go_first_pcm_write_at", time.Now()); tr.Snapshot() }()
	}
	wg.Wait()
}

func TestTurnBeginConcurrentIdentityAndEstimatedClock(t *testing.T) {
	c := NewTurnCollector(1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Begin(context.Background(), "same", time.Now()) }()
	}
	wg.Wait()
	if len(c.Snapshot()) != 1 || c.Dropped() != 0 {
		t.Fatal("duplicate trace")
	}
	tr := c.Find("same")
	end := time.Now()
	tr.Mark("lead_speech_end_at", end)
	tr.Mark("c_source_first_real_frame_at", end.Add(time.Second))
	tr.SetSpeechEndBasis("pcm_energy_estimate")
	if _, ok := tr.Snapshot().Metrics()["speech_end_to_first_agent_media_ms"]; ok {
		t.Fatal("estimated end presented as physical end")
	}
	if tr.Snapshot().Metrics()["estimated_speech_end_to_first_agent_media_ms"] != 1000 {
		t.Fatal("estimate absent")
	}
}
