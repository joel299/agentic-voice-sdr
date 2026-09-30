package voiceflow

import (
	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
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
	transcriber = geminilive.WithLeadTurnIdentity(transcriber, geminilive.NewLeadTurnSequencer(seedLeadSequence(state.Turns())))
	return bridge.NewSplit(input, output, transcriber, responder, handler, events, handler.Lifecycle()), nil
}

// NewSplitRuntimeWithTranscriptPersistence installs transcript persistence for
// one explicit canonical API call ID. Baresip media Session satisfies the
// bridge audio interfaces and can be passed directly; its socket/media identity
// never replaces callID.
func NewSplitRuntimeWithTranscriptPersistence(input bridge.AudioReader, output bridge.AudioWriter, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, state *conversation.ConversationState, processor turnloop.TurnProcessor, gate *conversation.ResponseGate, capability *turnruntime.CapabilityContext, events bridge.EventHandler, callID string, repository voicecalldomain.TranscriptRepository, sequencer ...*geminilive.LeadTurnSequencer) (*bridge.SplitBridge, error) {
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
	downstream = FinalAgentTranscriptHandler(callID, repository, handler.Lifecycle(), events)
	var owner *geminilive.LeadTurnSequencer
	if len(sequencer) > 0 {
		owner = sequencer[0]
	}
	if owner == nil {
		owner = geminilive.NewLeadTurnSequencer(seedLeadSequence(state.Turns()))
	}
	transcriber = geminilive.WithLeadTurnIdentity(transcriber, owner)
	return bridge.NewSplit(input, output, transcriber, responder, handler, downstream, handler.Lifecycle()), nil
}

// NewBaresipSplitRuntimeWithTranscriptPersistence is the GRU-152-ready
// composition boundary: CallService's canonical API callID is explicit and
// the call-scoped Baresip media session supplies only PCM transport.
func NewBaresipSplitRuntimeWithTranscriptPersistence(session *baresipmedia.Session, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, state *conversation.ConversationState, processor turnloop.TurnProcessor, gate *conversation.ResponseGate, capability *turnruntime.CapabilityContext, events bridge.EventHandler, callID string, repository voicecalldomain.TranscriptRepository, sequencer ...*geminilive.LeadTurnSequencer) (*bridge.SplitBridge, error) {
	if session == nil {
		return nil, bridge.ErrNilDependency
	}
	return NewSplitRuntimeWithTranscriptPersistence(session, session, transcriber, responder, state, processor, gate, capability, events, callID, repository, sequencer...)
}
