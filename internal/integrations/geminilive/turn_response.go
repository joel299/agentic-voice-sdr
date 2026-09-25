package geminilive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

const MaxTurnInstructionBytes = 2048

var (
	ErrTurnInstructionTooLarge = errors.New("geminilive: turn instruction exceeds limit")
	ErrInvalidTurnResponse     = errors.New("geminilive: invalid turn response")
	ErrNilTurnContext          = errors.New("geminilive: turn context is nil")
)

type turnInstruction struct {
	Action         conversation.NextAction `json:"action"`
	Reason         conversation.ReasonCode `json:"reason"`
	Terminal       bool                    `json:"terminal"`
	Handoff        bool                    `json:"handoff"`
	Capability     string                  `json:"capability,omitempty"`
	PolicyStatus   string                  `json:"policy_status,omitempty"`
	ExecutionState string                  `json:"execution_state"`
	ToolResult     string                  `json:"tool_result_status,omitempty"`
}

// RenderTurnDirective converts the canonical directive into a bounded control
// instruction. It carries outcome metadata only; Gemini owns the wording.
func RenderTurnDirective(directive conversation.TurnDirective) (string, error) {
	if err := directive.Validate(); err != nil {
		return "", fmt.Errorf("%w: directive: %w", ErrInvalidTurnResponse, err)
	}
	outcome := turnInstruction{
		Action:         directive.Kind,
		Reason:         directive.Reason,
		Terminal:       directive.Terminal,
		Handoff:        directive.Handoff,
		Capability:     directive.Capability,
		PolicyStatus:   string(directive.CapabilityStatus),
		ExecutionState: "not_applicable",
	}
	if directive.Capability != "" {
		if len([]byte(directive.Capability)) > MaxTurnInstructionBytes {
			return "", ErrTurnInstructionTooLarge
		}
		outcome.Capability = canonicalCapabilityOrUnknown(directive.Capability)
		switch directive.CapabilityStatus {
		case conversation.PolicyAllow:
			outcome.ExecutionState = "pending"
		case conversation.PolicyDeny, conversation.PolicyDefer:
			outcome.ExecutionState = "not_executed"
		}
	}
	if directive.ToolResult != nil {
		outcome.ExecutionState = "completed"
		outcome.ToolResult = string(directive.ToolResult.Status)
	}
	payload, err := json.Marshal(outcome)
	if err != nil {
		return "", fmt.Errorf("%w: marshal: %v", ErrInvalidTurnResponse, err)
	}
	instruction := "Generate a natural PT-BR voice response from this bounded turn outcome. Do not read the control payload aloud or invent data: " + string(payload)
	if len([]byte(instruction)) > MaxTurnInstructionBytes {
		return "", ErrTurnInstructionTooLarge
	}
	return instruction, nil
}

func canonicalCapabilityOrUnknown(capability string) string {
	for _, canonical := range tools.InitialToolNames() {
		if capability == canonical {
			return canonical
		}
	}
	return "unknown"
}

// SendTurnDirective renders and sends exactly one discrete Gemini response turn.
func (s *Session) SendTurnDirective(ctx context.Context, directive conversation.TurnDirective) error {
	if ctx == nil {
		return fmt.Errorf("%w: %w", ErrInvalidTurnResponse, ErrNilTurnContext)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	instruction, err := RenderTurnDirective(directive)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.SendClientContent(ctx, instruction)
}
