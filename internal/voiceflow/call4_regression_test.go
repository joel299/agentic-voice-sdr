package voiceflow

import (
	"context"
	"errors"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"testing"
	"time"
)

// Call #4: a second FINAL arrives during the first response, before TurnComplete.
// It must apply backpressure to the single transcript owner, not kill all AI loops.
func TestCall4FinalDuringActiveResponseWaitsWithoutKillingRuntime(t *testing.T) {
	h, state, processor := newTestHandler(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.HandleEvent(ctx, finalEvent("hello", "lead-000001")); err != nil {
		t.Fatal(err)
	}
	lease := h.Lifecycle().CaptureActive()
	done := make(chan error, 1)
	go func() {
		done <- h.HandleTranscript(ctx, geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "hello again", TurnID: "lead-000002"})
	}()
	select {
	case err := <-done:
		t.Fatalf("second FINAL terminated active response: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if len(state.Turns()) != 1 {
		t.Fatal("waiting FINAL changed the active response state")
	}
	if err := lease.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("waiting FINAL was not released")
	}
	if processor.calls != 2 {
		t.Fatalf("JEV calls=%d, want one per FINAL", processor.calls)
	}
	if err := h.Lifecycle().CaptureActive().Complete(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestCall4WaitingFinalCancellationKeepsExistingLease(t *testing.T) {
	h, _, p := newTestHandler(t, nil)
	if err := h.HandleEvent(context.Background(), finalEvent("hello", "lead-000001")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.HandleEvent(ctx, finalEvent("again", "lead-000002"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if p.calls != 1 || h.Lifecycle().CaptureActive() == nil {
		t.Fatal("canceled waiter changed active response")
	}
}

// Same trace as the owner call: intro audio, a second FINAL before completion,
// queue pressure, further AUDIO, then TurnComplete. No SIP/provider is contacted.
func TestCall4IntroSecondFinalKeepsAudioAndPersistsOnce(t *testing.T) {
	h, _, processor := newTestHandler(t, nil)
	repo := &transcriptRepoFake{}
	if err := h.WithTranscriptPersistence("call_fixture", repo); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.HandleEvent(ctx, finalEvent("hello", "lead-000001")); err != nil {
		t.Fatal(err)
	}
	lifecycle := h.Lifecycle()
	lease := lifecycle.CaptureActive()
	transcript := FinalAgentTranscriptHandler("call_fixture", repo, lifecycle, nil)
	if err := transcript(ctx, geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Sou o Joel. "}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	second := geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "hello again", TurnID: "lead-000002"}
	go func() { done <- h.HandleTranscript(ctx, second) }()
	// A FINAL is durable while blocked; no second JEV or lease is created yet.
	for {
		repo.mu.Lock()
		n := len(repo.turns)
		repo.mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("pending FINAL not durable")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !lease.ModelAudioAuthorized() {
		t.Fatal("pending FINAL revoked intro audio")
	}
	if err := transcript(ctx, geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Como posso ajudar?"}); err != nil {
		t.Fatal(err)
	}
	if err := transcript(ctx, geminilive.Event{Kind: geminilive.EventGenerationComplete}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		t.Fatalf("generationComplete released pending response: %v", e)
	default:
	}
	if err := transcript(ctx, geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := h.HandleTranscript(ctx, second); err != nil {
		t.Fatal(err)
	} // replay, no second JEV
	if processor.calls != 2 {
		t.Fatalf("JEV calls=%d", processor.calls)
	}
	rows, _ := repo.ListFinalTurns(ctx, "call_fixture", 20)
	if len(rows) != 3 || rows[2].Role != "agent" || rows[2].Text != "Sou o Joel. Como posso ajudar?" {
		t.Fatalf("rows=%+v", rows)
	}
}
