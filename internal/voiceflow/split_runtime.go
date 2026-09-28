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
// here; this function constructs Coordinator with that exact responder and
// wires the real TranscriptEvent handler and response lifecycle into
// bridge.SplitBridge. This makes Coordinator(responder A) plus
// SplitBridge(responder B) impossible through this composition boundary.
func NewSplitRuntime(input bridge.AudioReader, output bridge.AudioWriter, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, state *conversation.ConversationState, processor turnloop.TurnProcessor, gate *conversation.ResponseGate, capability *turnruntime.CapabilityContext, events bridge.EventHandler) (*bridge.SplitBridge, error) {
	coordinator, err := turnloop.New(processor, responder, gate)
	if err != nil {
		return nil, err
	}
	handler, err := NewFinalTranscriptHandler(state, coordinator, capability)
	if err != nil {
		return nil, err
	}
	return bridge.NewSplit(input, output, transcriber, responder, handler, events, handler.Lifecycle()), nil
}
