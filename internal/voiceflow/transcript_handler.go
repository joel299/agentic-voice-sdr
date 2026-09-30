package voiceflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
	"github.com/joel299/agentic-voice-sdr/internal/turnloop"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

var (
	ErrInvalidHandler        = errors.New("voiceflow: invalid transcript handler")
	ErrTranscriptPersistence = errors.New("voiceflow: final transcript persistence failed")
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
	transcripts voicecalldomain.TranscriptRepository
	callID      string
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
		nextLead: seedLeadSequence(state.Turns()),
	}, nil
}

func seedLeadSequence(turns []conversation.Turn) uint64 {
	var max uint64
	for _, turn := range turns {
		if !strings.HasPrefix(turn.ID, "lead-") {
			continue
		}
		suffix := strings.TrimPrefix(turn.ID, "lead-")
		if len(suffix) != 6 {
			continue
		}
		var value uint64
		for _, char := range suffix {
			if char < '0' || char > '9' {
				value = 0
				break
			}
			value = value*10 + uint64(char-'0')
		}
		if value > max {
			max = value
		}
	}
	return max
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
// HandleTranscript consumes the real input-session boundary directly. Interim
// events are intentionally ignored and final events are never converted into
// a provider response Event.
func (h *FinalTranscriptHandler) HandleTranscript(ctx context.Context, event geminilive.TranscriptEvent) error {
	if h == nil || h.state == nil || h.coordinator == nil {
		return ErrInvalidHandler
	}
	if event.State != geminilive.TranscriptFinal {
		return nil
	}
	return h.handleFinalText(ctx, event.Text, event.TurnID)
}

func (h *FinalTranscriptHandler) HandleEvent(ctx context.Context, event geminilive.Event) error {
	if h == nil || h.state == nil || h.coordinator == nil {
		return ErrInvalidHandler
	}
	if event.Kind != geminilive.EventInputTranscription || event.InputTranscriptState != geminilive.TranscriptFinal {
		return h.forward(ctx, event)
	}
	return h.handleFinalText(ctx, event.Text, event.TurnID)
}

// WithTranscriptPersistence enables durable storage for this call. Stable
// application turn IDs, not provider receive ordinals, form idempotency keys.
func (h *FinalTranscriptHandler) WithTranscriptPersistence(callID string, repository voicecalldomain.TranscriptRepository) error {
	if h == nil || callID == "" || repository == nil {
		return ErrInvalidHandler
	}
	h.callID, h.transcripts = callID, repository
	return nil
}

func (h *FinalTranscriptHandler) handleFinalText(ctx context.Context, rawText, turnID string) error {
	text := strings.TrimSpace(rawText)
	if text == "" {
		return nil
	}
	if turnID == "" {
		turnID = fmt.Sprintf("lead-%06d", h.nextLead+1)
	}
	if h.transcripts != nil {
		_, created, err := h.transcripts.AppendFinalTurn(ctx, h.callID, "lead", text, "gemini_input", "lead:"+h.callID+":"+turnID)
		if err != nil {
			return fmt.Errorf("%w", ErrTranscriptPersistence)
		}
		if !created {
			return nil
		}
	}

	h.nextLead++
	turn, err := conversation.NewTurn(turnID, conversation.RoleLead, text, conversation.TranscriptFinal)
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
	h.lifecycle.Bind(key)
	return nil
}

func (h *FinalTranscriptHandler) forward(ctx context.Context, event geminilive.Event) error {
	if h.downstream == nil {
		return nil
	}
	return h.downstream(ctx, event)
}
