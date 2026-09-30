// Package voicecall defines durable call and transcript records.
package voicecall

import (
	"context"
	"time"
)

type Call struct {
	ID, Destination, Status, Provider string
	ProviderCallID                    string
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
	GetCall(context.Context, string) (Call, error)
}

type TranscriptRepository interface {
	AppendFinalTurn(context.Context, string, string, string, string, string) (Turn, bool, error)
	ListFinalTurns(context.Context, string) ([]Turn, error)
}
