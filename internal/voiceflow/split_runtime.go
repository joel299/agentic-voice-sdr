package voiceflow

import (
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
	"github.com/joel299/agentic-voice-sdr/internal/turnloop"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

// NewSplitRuntime is the executable composition boundary for the two-session
// telephony path. The caller builds Coordinator with the same responder passed
// here; this function wires the real TranscriptEvent handler and response
// lifecycle into bridge.SplitBridge without adapting either session to the old
// single-session GeminiSession interface.
func NewSplitRuntime(input bridge.AudioReader, output bridge.AudioWriter, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, state *conversation.ConversationState, coordinator *turnloop.Coordinator, capability *turnruntime.CapabilityContext, events bridge.EventHandler) (*bridge.SplitBridge, error) {
	handler, err := NewFinalTranscriptHandler(state, coordinator, capability)
	if err != nil {
		return nil, err
	}
	return bridge.NewSplit(input, output, transcriber, responder, handler, events, handler.Lifecycle()), nil
}
