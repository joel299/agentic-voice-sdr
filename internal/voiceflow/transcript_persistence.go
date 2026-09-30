package voiceflow

import (
	"context"
	"strings"
	"sync"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
)

// agentTranscriptAccumulator joins incremental output transcription chunks
// into one final transcript turn. Only an authorized response lease can own or
// finalize the accumulator; interruptions and unowned events discard it.
type agentTranscriptAccumulator struct {
	callID     string
	repo       voicecalldomain.TranscriptRepository
	lifecycle  bridge.ResponseLifecycle
	downstream func(context.Context, geminilive.Event) error

	mu     sync.Mutex
	turnID string
	text   strings.Builder
}

func FinalAgentTranscriptHandler(callID string, repository voicecalldomain.TranscriptRepository, lifecycle bridge.ResponseLifecycle, downstream func(context.Context, geminilive.Event) error) func(context.Context, geminilive.Event) error {
	a := &agentTranscriptAccumulator{callID: callID, repo: repository, lifecycle: lifecycle, downstream: downstream}
	return a.Handle
}

func (a *agentTranscriptAccumulator) Handle(ctx context.Context, event geminilive.Event) error {
	switch event.Kind {
	case geminilive.EventOutputTranscription:
		turnID := a.authorizedTurnID()
		if turnID == "" {
			a.discard()
			break
		}
		a.mu.Lock()
		if a.turnID != turnID {
			a.turnID = turnID
			a.text.Reset()
		}
		a.text.WriteString(event.Text)
		a.mu.Unlock()
		if event.TurnComplete {
			if err := a.finalize(ctx, turnID); err != nil {
				return err
			}
		}
	case geminilive.EventTurnComplete:
		if turnID := a.authorizedTurnID(); turnID != "" {
			if err := a.finalize(ctx, turnID); err != nil {
				return err
			}
		} else {
			a.discard()
		}
	case geminilive.EventInterrupted, geminilive.EventAPIError, geminilive.EventClosed:
		a.discard()
	}
	if event.TurnComplete && event.Kind != geminilive.EventOutputTranscription && event.Kind != geminilive.EventTurnComplete {
		if turnID := a.authorizedTurnID(); turnID != "" {
			if err := a.finalize(ctx, turnID); err != nil {
				return err
			}
		} else {
			a.discard()
		}
	}
	if a.downstream != nil {
		return a.downstream(ctx, event)
	}
	return nil
}

func (a *agentTranscriptAccumulator) authorizedTurnID() string {
	if a == nil || a.callID == "" || a.repo == nil || a.lifecycle == nil {
		return ""
	}
	lease := a.lifecycle.CaptureActive()
	if lease == nil || !lease.ModelAudioAuthorized() {
		return ""
	}
	identity, ok := lease.(bridge.ResponseTurnIdentity)
	if !ok {
		return ""
	}
	return strings.TrimSpace(identity.ResponseTurnID())
}

func (a *agentTranscriptAccumulator) finalize(ctx context.Context, turnID string) error {
	a.mu.Lock()
	if turnID == "" || a.turnID != turnID {
		a.mu.Unlock()
		return nil
	}
	text := strings.TrimSpace(a.text.String())
	a.turnID = ""
	a.text.Reset()
	a.mu.Unlock()
	if text == "" {
		return nil
	}
	key := "agent:" + a.callID + ":" + turnID
	if _, _, err := a.repo.AppendFinalTurn(ctx, a.callID, "agent", text, "gemini_output", key); err != nil {
		return ErrTranscriptPersistence
	}
	return nil
}

func (a *agentTranscriptAccumulator) discard() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.turnID = ""
	a.text.Reset()
	a.mu.Unlock()
}
