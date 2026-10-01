// Package voicecall defines durable call and transcript records.
package voicecall

import (
	"context"
	"errors"
	"time"
)

var ErrCallNotFound = errors.New("call not found")

type Call struct {
	ID, Destination, Status, Provider string
	ProviderCallID                    string
	AIRuntimeStatus                   string
	AIFailureClass                    string
	StartedAt, ConnectedAt, EndedAt   *time.Time
	TerminalReason                    string
	CreatedAt, UpdatedAt              time.Time
}

type Turn struct {
	ID, CallID, Role, Text, State, Source, IdempotencyKey string
	Sequence                                              int64
	CreatedAt                                             time.Time
}

type CallRepository interface {
	CreateCall(context.Context, Call) error
	UpdateLifecycle(context.Context, string, string, string, string) error
	UpdateAIRuntimeStatus(context.Context, string, string, string) error
	GetCall(context.Context, string) (Call, error)
}

type TranscriptRepository interface {
	AppendFinalTurn(context.Context, string, string, string, string, string) (Turn, bool, error)
	ListFinalTurns(context.Context, string, int) ([]Turn, error)
}
