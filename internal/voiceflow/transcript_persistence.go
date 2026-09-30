package voiceflow

import (
	"context"
	"fmt"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

// FinalAgentTranscriptHandler persists only finalized Gemini output text. It
// can be installed at the existing bridge event boundary; it stores no audio
// frames, prompts, or provider JSON.
func FinalAgentTranscriptHandler(callID string, repository voicecalldomain.TranscriptRepository, downstream func(context.Context, geminilive.Event) error) func(context.Context, geminilive.Event) error {
	return func(ctx context.Context, event geminilive.Event) error {
		if event.Kind == geminilive.EventOutputTranscription {
			if callID == "" || repository == nil || event.EventID == "" {
				return ErrTranscriptPersistence
			}
			if _, _, err := repository.AppendFinalTurn(ctx, callID, "agent", event.Text, "gemini_output", "agent:"+event.EventID); err != nil {
				return fmt.Errorf("%w", ErrTranscriptPersistence)
			}
		}
		if downstream != nil {
			return downstream(ctx, event)
		}
		return nil
	}
}
