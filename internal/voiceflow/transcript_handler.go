package voiceflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
	"github.com/joel299/agentic-voice-sdr/internal/turnloop"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

var (
	ErrInvalidHandler = errors.New("voiceflow: invalid transcript handler")
)

// EventHandler is the subset of bridge.EventHandler needed by the driver.
type EventHandler = bridge.EventHandler

// FinalTranscriptHandler wires Gemini final input transcripts into the
// synchronous turn coordinator. The receive owner must call HandleEvent
// synchronously; this type intentionally starts no goroutines.
type FinalTranscriptHandler struct {
	state       *conversation.ConversationState
	coordinator *turnloop.Coordinator
	capability  *turnruntime.CapabilityContext
	downstream  bridge.EventHandler
	lifecycle   *ResponseLifecycleAdapter
	nextLead    uint64
}

// NewFinalTranscriptHandler creates a handler with a fixed capability context.
// A nil capability is valid for turns that do not request a capability.
func NewFinalTranscriptHandler(state *conversation.ConversationState, coordinator *turnloop.Coordinator, capability *turnruntime.CapabilityContext, downstream ...bridge.EventHandler) (*FinalTranscriptHandler, error) {
	if state == nil || coordinator == nil {
		return nil, fmt.Errorf("%w: state and coordinator are required", ErrInvalidHandler)
	}
	var next bridge.EventHandler
	if len(downstream) > 0 {
		next = downstream[0]
	}
	return &FinalTranscriptHandler{
		state: state, coordinator: coordinator, capability: capability,
		downstream: next, lifecycle: NewResponseLifecycleAdapter(coordinator),
	}, nil
}

// Lifecycle returns the concrete bridge lifecycle adapter owned by this
// handler. It is safe to pass directly to bridge.New.
func (h *FinalTranscriptHandler) Lifecycle() bridge.ResponseLifecycle {
	if h == nil {
		return nil
	}
	return h.lifecycle
}

// NewHandler is a concise compatibility constructor for session-runtime code.
func NewHandler(state *conversation.ConversationState, coordinator *turnloop.Coordinator, capability *turnruntime.CapabilityContext, downstream ...bridge.EventHandler) (*FinalTranscriptHandler, error) {
	return NewFinalTranscriptHandler(state, coordinator, capability, downstream...)
}

// HandleEvent ignores interim transcripts for turnloop purposes. Final valid
// transcripts create one new lead turn and synchronously begin exactly one
// response. Other events retain their existing downstream behavior.
func (h *FinalTranscriptHandler) HandleEvent(ctx context.Context, event geminilive.Event) error {
	if h == nil || h.state == nil || h.coordinator == nil {
		return ErrInvalidHandler
	}
	if event.Kind != geminilive.EventInputTranscription || event.InputTranscriptState != geminilive.TranscriptFinal {
		return h.forward(ctx, event)
	}
	if strings.TrimSpace(event.Text) == "" {
		return h.forward(ctx, event)
	}

	h.nextLead++
	turnID := fmt.Sprintf("lead-%06d", h.nextLead)
	turn, err := conversation.NewTurn(turnID, conversation.RoleLead, event.Text, conversation.TranscriptFinal)
	if err != nil {
		return err
	}
	if _, err := h.state.RecordTurn(turn); err != nil {
		return err
	}
	key, err := h.coordinator.Begin(ctx, h.state, turnID, h.capability)
	if err != nil {
		return err
	}
	// Begin is synchronous; bind before returning to the receive owner so the
	// bridge can authorize the very first model audio chunk.
	h.lifecycle.Bind(key)
	return h.forward(ctx, event)
}

func (h *FinalTranscriptHandler) forward(ctx context.Context, event geminilive.Event) error {
	if h.downstream == nil {
		return nil
	}
	return h.downstream(ctx, event)
}
