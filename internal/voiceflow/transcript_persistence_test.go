package voiceflow

import (
	"context"
	"errors"
	"testing"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

type transcriptRepoFake struct {
	turns []voicecalldomain.Turn
	err   error
}

func (r *transcriptRepoFake) AppendFinalTurn(_ context.Context, callID, role, text, source, key string) (voicecalldomain.Turn, bool, error) {
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
func (*transcriptRepoFake) ListFinalTurns(context.Context, string) ([]voicecalldomain.Turn, error) {
	return nil, nil
}

func TestLeadTranscriptPersistenceIgnoresInterimAndSurfacesFailure(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	repo := &transcriptRepoFake{}
	if err := h.WithTranscriptPersistence("call-1", repo); err != nil {
		t.Fatal(err)
	}
	if err := h.HandleTranscript(context.Background(), geminilive.TranscriptEvent{State: geminilive.TranscriptInterim, Text: "Olá eu...", EventID: "e1"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 0 {
		t.Fatalf("interim persisted: %+v", repo.turns)
	}
	event := geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Olá, eu gostaria de informações.", EventID: "e2"}
	if err := h.HandleTranscript(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || repo.turns[0].Role != "lead" || repo.turns[0].Source != "gemini_input" || repo.turns[0].State != "final" {
		t.Fatalf("final turn=%+v", repo.turns)
	}
	if err := h.HandleTranscript(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("replayed final duplicated: %+v", repo.turns)
	}
	repo.err = errors.New("db unavailable")
	if err := h.HandleTranscript(context.Background(), geminilive.TranscriptEvent{State: geminilive.TranscriptFinal, Text: "Outro turno", EventID: "e3"}); !errors.Is(err, ErrTranscriptPersistence) {
		t.Fatalf("write failure=%v", err)
	}
}

func TestAgentOutputTranscriptionPersistence(t *testing.T) {
	repo := &transcriptRepoFake{}
	handler := FinalAgentTranscriptHandler("call-1", repo, nil)
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventOutputTranscription, Text: "Claro, posso te explicar.", EventID: "out-1"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 || repo.turns[0].Role != "agent" || repo.turns[0].Source != "gemini_output" || repo.turns[0].Sequence != 1 {
		t.Fatalf("agent turn=%+v", repo.turns)
	}
	if err := handler(context.Background(), geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.turns) != 1 {
		t.Fatalf("audio caused persistence: %+v", repo.turns)
	}
}
