package voiceflow

import (
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
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

// NewSplitRuntimeWithTranscriptPersistence installs the existing final lead
// transcript handler and the controlled-session output event boundary with
// one explicit durable call ID. No call ID is inferred from media or text.
func NewSplitRuntimeWithTranscriptPersistence(input bridge.AudioReader, output bridge.AudioWriter, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, state *conversation.ConversationState, processor turnloop.TurnProcessor, gate *conversation.ResponseGate, capability *turnruntime.CapabilityContext, events bridge.EventHandler, callID string, repository voicecalldomain.TranscriptRepository) (*bridge.SplitBridge, error) {
	coordinator, err := turnloop.New(processor, responder, gate)
	if err != nil {
		return nil, err
	}
	handler, err := NewFinalTranscriptHandler(state, coordinator, capability)
	if err != nil {
		return nil, err
	}
	if err := handler.WithTranscriptPersistence(callID, repository); err != nil {
		return nil, err
	}
	var downstream bridge.EventHandler
	downstream = FinalAgentTranscriptHandler(callID, repository, events)
	return bridge.NewSplit(input, output, transcriber, responder, handler, downstream, handler.Lifecycle()), nil
}
