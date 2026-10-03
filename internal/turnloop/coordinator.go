// Package turnloop coordinates ownership and lifecycle for one finalized lead turn.
package turnloop

import (
	"context"
	"errors"
	"fmt"
	"github.com/joel299/agentic-voice-sdr/internal/telemetry"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

var (
	ErrInvalidCoordinator      = errors.New("invalid turn response coordinator")
	ErrInvalidCoordinatorInput = errors.New("invalid turn response coordinator input")
)

// TurnProcessor is the narrow provider-agnostic turn runtime boundary.
type TurnProcessor interface {
	ProcessTurn(context.Context, turnruntime.TurnInput) (conversation.TurnDirective, error)
}

// TurnResponder is the narrow controlled provider send boundary.
type TurnResponder interface {
	SendControlledTurn(context.Context, string, conversation.TurnDirective) error
}

type StageError struct {
	Stage string
	Cause error
}

func (e *StageError) Error() string   { return "turn loop stage failed: " + e.Stage }
func (e *StageError) Unwrap() error   { return e.Cause }
func (e *StageError) AIStage() string { return e.Stage }

// Coordinator claims response ownership before processing a turn and keeps the
// lifecycle state until the provider receive owner completes or fails it.
type Coordinator struct {
	processor TurnProcessor
	responder TurnResponder
	gate      *conversation.ResponseGate
}

func (c *Coordinator) reportStage(stage, outcome string) {
	if c == nil {
		return
	}
	if reporter, ok := c.processor.(interface{ ReportStage(string, string) }); ok {
		reporter.ReportStage(stage, outcome)
	}
}

func New(processor TurnProcessor, responder TurnResponder, gate *conversation.ResponseGate) (*Coordinator, error) {
	if processor == nil || responder == nil || gate == nil {
		return nil, fmt.Errorf("%w: processor, responder, and gate are required", ErrInvalidCoordinator)
	}
	return &Coordinator{processor: processor, responder: responder, gate: gate}, nil
}

// Begin reserves the finalized source turn before allowing the runtime to run.
// A successful send intentionally leaves the response in Started until the
// existing provider event owner calls Complete or Fail.
func (c *Coordinator) Begin(ctx context.Context, state *conversation.ConversationState, sourceTurnID string, capability *turnruntime.CapabilityContext) (conversation.ResponseKey, error) {
	if c == nil || c.processor == nil || c.responder == nil || c.gate == nil {
		return conversation.ResponseKey{}, ErrInvalidCoordinator
	}
	if ctx == nil || state == nil || strings.TrimSpace(sourceTurnID) == "" {
		return conversation.ResponseKey{}, ErrInvalidCoordinatorInput
	}
	if err := ctx.Err(); err != nil {
		return conversation.ResponseKey{}, err
	}

	reservation, err := c.gate.Reserve(state, sourceTurnID)
	if err != nil {
		return conversation.ResponseKey{}, err
	}

	directive, err := c.processor.ProcessTurn(ctx, turnruntime.TurnInput{
		State:      state,
		Capability: capability,
	})
	if err != nil {
		c.failAfterReservation(reservation.Key)
		return conversation.ResponseKey{}, err
	}
	if err := ctx.Err(); err != nil {
		c.failAfterReservation(reservation.Key)
		return conversation.ResponseKey{}, err
	}

	if _, err := c.gate.AuthorizeReserved(reservation.Key, directive); err != nil {
		c.failAfterReservation(reservation.Key)
		return conversation.ResponseKey{}, err
	}
	if err := c.gate.Start(reservation.Key); err != nil {
		c.failAfterReservation(reservation.Key)
		return conversation.ResponseKey{}, err
	}
	finalLeadText, ok := finalizedLeadText(state, sourceTurnID)
	if !ok {
		c.failAfterReservation(reservation.Key)
		return conversation.ResponseKey{}, ErrInvalidCoordinatorInput
	}
	telemetry.MarkTurn(ctx, "turn_directive_sent_at")
	c.reportStage("gemini_response_send", "started")
	if err := c.responder.SendControlledTurn(ctx, finalLeadText, directive); err != nil {
		c.failAfterReservation(reservation.Key)
		c.reportStage("gemini_response_send", "failed")
		return conversation.ResponseKey{}, &StageError{Stage: "gemini_response_send", Cause: err}
	}
	c.reportStage("controlled_response_sent", "completed")
	return reservation.Key, nil
}

// Complete is called by the existing provider event owner after TurnComplete.
func (c *Coordinator) Complete(key conversation.ResponseKey) error {
	if c == nil || c.gate == nil {
		return ErrInvalidCoordinator
	}
	return c.gate.Complete(key)
}

// Fail is called by the existing provider event owner for a failed response.
func (c *Coordinator) Fail(key conversation.ResponseKey) error {
	if c == nil || c.gate == nil {
		return ErrInvalidCoordinator
	}
	return c.gate.Fail(key)
}

func (c *Coordinator) failAfterReservation(key conversation.ResponseKey) {
	_ = c.gate.Fail(key)
}

func finalizedLeadText(state *conversation.ConversationState, sourceTurnID string) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, turn := range state.Turns() {
		if turn.ID == sourceTurnID {
			return turn.Text, turn.Role == conversation.RoleLead && turn.Transcript == conversation.TranscriptFinal
		}
	}
	return "", false
}
