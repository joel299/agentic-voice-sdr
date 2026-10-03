package geminilive

import (
	"context"
	"errors"
	"testing"
)

func TestVADConfigIsInputOnlyAndSafe(t *testing.T) {
	cfg := Config{VAD: VADConfig{SilenceDurationMS: 600, PrefixPaddingMS: 40, EndSensitivity: "END_SENSITIVITY_LOW"}}
	s := setupMessage(cfg, roleInputTranscription)["setup"].(map[string]any)
	if s["realtimeInputConfig"] == nil {
		t.Fatal("VAD tuning absent")
	}
	s = setupMessage(cfg, roleControlledResponse)["setup"].(map[string]any)
	if s["realtimeInputConfig"] != nil {
		t.Fatal("response role VAD leak")
	}
	if e := (VADConfig{SilenceDurationMS: 100}).Validate(); e == nil {
		t.Fatal("unsafe silence accepted")
	}
}

func TestHybridVADPreservesPauseAndFlushesOnlyOnce(t *testing.T) {
	s := &vadFixtureInput{}
	h := WithHybridVAD(s, 1200)
	voiced := make([]byte, 640)
	for i := 0; i < len(voiced); i += 2 {
		voiced[i] = 0x10
		voiced[i+1] = 0x10
	}
	quiet := make([]byte, 640)
	ctx := context.Background()
	if e := h.SendAudio(ctx, []byte{1}); e == nil {
		t.Fatal("odd PCM accepted")
	}
	h.SendAudio(ctx, voiced)
	for i := 0; i < 50; i++ {
		h.SendAudio(ctx, quiet)
	}
	if s.ends != 0 {
		t.Fatal("cut one second pause")
	}
	h.SendAudio(ctx, voiced)
	for i := 0; i < 100; i++ {
		h.SendAudio(ctx, quiet)
	}
	if s.ends != 1 {
		t.Fatalf("flush count=%d", s.ends)
	}
	h.SendAudio(ctx, voiced)
	for i := 0; i < 60; i++ {
		h.SendAudio(ctx, quiet)
	}
	if s.ends != 2 {
		t.Fatal("next phrase did not reopen")
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if e := h.SendAudio(c, voiced); !errors.Is(e, context.Canceled) {
		t.Fatal("context ignored")
	}
}

type vadFixtureInput struct{ sends, ends int }

func (s *vadFixtureInput) SendAudio(context.Context, []byte) error { s.sends++; return nil }
func (s *vadFixtureInput) EndAudio(context.Context) error          { s.ends++; return nil }
func (*vadFixtureInput) Receive(context.Context) (TranscriptEvent, error) {
	return TranscriptEvent{}, context.Canceled
}
func (*vadFixtureInput) Close() error { return nil }
