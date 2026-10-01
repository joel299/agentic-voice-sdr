package voiceflow

import (
	"context"
	"errors"
	"sync"
	"testing"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
)

type transcriptRepoFake struct {
	mu    sync.Mutex
	turns []voicecalldomain.Turn
	err   error
}

func (r *transcriptRepoFake) AppendFinalTurn(_ context.Context, callID, role, text, source, key string) (voicecalldomain.Turn, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return voicecalldomain.Turn{}, false, r.err
	}
	for _, t := range r.turns {
		if t.IdempotencyKey == key {
			return t, false, nil
		}
	}
	t := voicecalldomain.Turn{CallID: callID, Role: role, Text: text, Source: source, State: "final", Sequence: int64(len(r.turns) + 1), IdempotencyKey: key}
	r.turns = append(r.turns, t)
	return t, true, nil
}
func (r *transcriptRepoFake) ListFinalTurns(context.Context, string, int) ([]voicecalldomain.Turn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]voicecalldomain.Turn(nil), r.turns...), nil
}

func TestLeadTranscriptPersistenceIgnoresInterimAndSurfacesFailure(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	repo := &transcriptRepoFake{}
	if err := h.WithTranscriptPersistence("call_X", repo); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleTranscript(context.Background(), geminilive.TranscriptEvent{State: geminilive.TranscriptInterim, Text: "Olá eu...", EventID: "receive-1"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 0 {
		t.Fatalf("interim persisted: %+v", repo.turns)
	}
	event := geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Olá, eu gostaria de informações.", EventID: "provider-event-1", TurnID: "lead-000001"}
	if err := h.HandleTranscript(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || repo.turns[0].CallID != "call_X" || repo.turns[0].Role != "lead" || repo.turns[0].Source != "gemini_input" || repo.turns[0].State != "final" || repo.turns[0].IdempotencyKey != "lead:call_X:lead-000001" {
		t.Fatalf("final turn=%+v", repo.turns)
	}
	if err := h.HandleTranscript(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("replayed final duplicated: %+v", repo.turns)
	}
	repo.err = errors.New("db unavailable")
	if err := h.HandleTranscript(context.Background(), geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Outro turno", EventID: "provider-stable-id", TurnID: "lead-000002"}); !errors.Is(err, ErrTranscriptPersistence) {
		t.Fatalf("write failure=%v", err)
	}
}

func TestFinalTranscriptHandlerRejectsMissingApplicationTurnID(t *testing.T) {
	h, _, processor := newTestHandler(t, nil)
	repo := &transcriptRepoFake{}
	if err := h.WithTranscriptPersistence("call_X", repo); err != nil {
		t.Fatal(err)
	}
	err := h.HandleTranscript(context.Background(), geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Sim", EventID: "receive-1"})
	if !errors.Is(err, ErrMissingLeadTurnID) {
		t.Fatalf("missing application turn identity error=%v", err)
	}
	if len(repo.turns) != 0 || len(h.state.Turns()) != 0 || processor.calls != 0 {
		t.Fatalf("missing ID caused effects: rows=%+v turns=%+v begins=%d", repo.turns, h.state.Turns(), processor.calls)
	}
}

func TestLeadTurnIdentityIsApplicationOwnedAcrossGeminiSessionRestart(t *testing.T) {
	repo := &transcriptRepoFake{}
	h1, state, _ := newTestHandler(t, nil)
	if err := h1.WithTranscriptPersistence("call_X", repo); err != nil {
		t.Fatal(err)
	}
	first := geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Sim", EventID: "receive-1", TurnID: "lead-000001"}
	if err := h1.HandleTranscript(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	// A new Gemini session may restart its synthetic receive ordinal, but the
	// application state owns the next call-scoped lead turn ID.
	h2, _, _ := newTestHandler(t, nil)
	h2.state = state
	if err := h2.WithTranscriptPersistence("call_X", repo); err != nil {
		t.Fatal(err)
	}
	second := geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Sim", EventID: "receive-1", TurnID: "lead-000002"}
	if err := h2.HandleTranscript(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 2 || repo.turns[0].IdempotencyKey == repo.turns[1].IdempotencyKey || repo.turns[0].Text != "Sim" || repo.turns[1].Text != "Sim" {
		t.Fatalf("same text in distinct application turns must remain distinct: %+v", repo.turns)
	}
}

func TestFinalOnlyUtterancesPersistAndBeginAsDistinctTurns(t *testing.T) {
	handler, state, processor := newTestHandler(t, nil)
	repo := &transcriptRepoFake{}
	if err := handler.WithTranscriptPersistence("call_X", repo); err != nil {
		t.Fatal(err)
	}
	input := &e2eInput{events: make(chan geminilive.TranscriptEvent, 3), seen: make(chan geminilive.TranscriptEvent, 3)}
	owned := geminilive.WithLeadTurnIdentity(input, geminilive.NewLeadTurnSequencer(0))
	for i, test := range []struct{ text, want string }{
		{text: "Sim", want: "lead-000001"},
		{text: "Não", want: "lead-000002"},
		{text: "Sim", want: "lead-000003"},
	} {
		input.events <- geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: test.text, EventID: "receive-1"}
		event, err := owned.Receive(context.Background())
		if err != nil || event.TurnID != test.want {
			t.Fatalf("turn %d identity=%q want %q; event=%+v err=%v", i+1, event.TurnID, test.want, event, err)
		}
		if err := handler.HandleTranscript(context.Background(), event); err != nil {
			t.Fatalf("handle turn %d: %v", i+1, err)
		}
		lease := handler.Lifecycle().CaptureActive()
		if lease == nil {
			t.Fatalf("turn %d did not begin a response lifecycle", i+1)
		}
		if err := lease.Complete(context.Background()); err != nil {
			t.Fatalf("complete turn %d before next utterance: %v", i+1, err)
		}
	}
	turns := state.Turns()
	if len(turns) != 3 || len(repo.turns) != 3 || processor.calls != 3 {
		t.Fatalf("state turns=%+v persisted rows=%+v processor begins=%d; want 3 each", turns, repo.turns, processor.calls)
	}
	for i, want := range []string{"lead-000001", "lead-000002", "lead-000003"} {
		if turns[i].ID != want || repo.turns[i].IdempotencyKey != "lead:call_X:"+want {
			t.Fatalf("turn %d state=%+v row=%+v", i+1, turns[i], repo.turns[i])
		}
	}
}

type transcriptTestLifecycle struct {
	active bool
	turnID string
}

func (l *transcriptTestLifecycle) CaptureActive() bridge.ResponseTurnLease {
	if !l.active {
		return nil
	}
	return transcriptTestLease{turnID: l.turnID}
}
func (l *transcriptTestLifecycle) FailActive(context.Context, error) error {
	l.active = false
	return nil
}

type transcriptTestLease struct{ turnID string }

func (transcriptTestLease) ModelAudioAuthorized() bool        { return true }
func (transcriptTestLease) Complete(context.Context) error    { return nil }
func (transcriptTestLease) Fail(context.Context, error) error { return nil }
func (l transcriptTestLease) ResponseTurnID() string          { return l.turnID }

func TestAgentOutputTranscriptionAggregatesOnlyAuthorizedCompletedResponse(t *testing.T) {
	repo := &transcriptRepoFake{}
	lifecycle := &transcriptTestLifecycle{active: true, turnID: "lead-000001"}
	handler := FinalAgentTranscriptHandler("call_X", repo, lifecycle, nil)
	for _, chunk := range []string{"Claro,", " posso"} {
		if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: chunk, EventID: "receive-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.turns) != 0 {
		t.Fatalf("partial chunks persisted before turn completion: %+v", repo.turns)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: " ajudar.", EventID: "receive-2", TurnComplete: true}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || repo.turns[0].CallID != "call_X" || repo.turns[0].Text != "Claro, posso ajudar." || repo.turns[0].Role != "agent" || repo.turns[0].Source != "gemini_output" || repo.turns[0].Sequence != 1 || repo.turns[0].IdempotencyKey != "agent:call_X:lead-000001" {
		t.Fatalf("agent turn=%+v", repo.turns)
	}
	// Replaying the same logical response turn is idempotent even when its
	// Gemini receive event ordinals start over.
	for _, chunk := range []string{"Claro,", " posso", " ajudar."} {
		if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: chunk, EventID: "receive-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("replayed response created duplicate: %+v", repo.turns)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("audio caused persistence: %+v", repo.turns)
	}
	lifecycle.active = false
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "stray"}); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("unowned transcript persisted: %+v", repo.turns)
	}

	// A second authorized turn with the same text has its own stable identity.
	lifecycle.active = true
	lifecycle.turnID = "lead-000002"
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Sim"}); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 2 || repo.turns[1].Text != "Sim" {
		t.Fatalf("distinct response turn not persisted: %+v", repo.turns)
	}
	if repo.turns[0].Text == repo.turns[1].Text && repo.turns[0].IdempotencyKey == repo.turns[1].IdempotencyKey {
		t.Fatal("distinct response turns collided")
	}

	lifecycle.active = true
	lifecycle.turnID = "lead-000003"
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "incomplete"}); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventInterrupted}); err != nil {
		t.Fatal(err)
	}
	lifecycle.active = false
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 2 {
		t.Fatalf("interrupted response was finalized: %+v", repo.turns)
	}

	repo.err = errors.New("database down")
	lifecycle.active = true
	lifecycle.turnID = "lead-000004"
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "persist failure"}); err != nil {
		t.Fatal(err)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventTurnComplete}); !errors.Is(err, ErrTranscriptPersistence) {
		t.Fatalf("persistence error not surfaced: %v", err)
	}
	if len(repo.turns) != 2 {
		t.Fatalf("failed write changed existing turns: %+v", repo.turns)
	}
}
