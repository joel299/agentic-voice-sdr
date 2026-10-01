// Package transcriptquery provides a narrow query boundary for public call
// transcript reads.
package transcriptquery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
)

const (
	DefaultLimit = 100
	MaxLimit     = 500
)

var (
	ErrCallNotFound = errors.New("call not found")
	ErrUnavailable  = errors.New("transcript query unavailable")
	ErrInvalidCall  = errors.New("invalid call id")
)

type CallReader interface {
	GetCall(context.Context, string) (voicecalldomain.Call, error)
}

type FinalTurnReader interface {
	ListFinalTurns(context.Context, string, int) ([]voicecalldomain.Turn, error)
}

type Transcript struct {
	CallID string
	Status string
	Turns  []Turn
}

// Turn contains only fields allowed in the public transcript response.
type Turn struct {
	Sequence  int64
	Role      string
	Text      string
	CreatedAt time.Time
}

// Service combines the durable call lifecycle and bounded final-turn readers.
type Service struct {
	calls      CallReader
	transcript FinalTurnReader
}

func New(calls CallReader, transcript FinalTurnReader) *Service {
	return &Service{calls: calls, transcript: transcript}
}

func (s *Service) Get(ctx context.Context, callID string, limit int) (Transcript, error) {
	if ctx == nil || strings.TrimSpace(callID) == "" {
		return Transcript{}, ErrInvalidCall
	}
	if s == nil || s.calls == nil || s.transcript == nil {
		return Transcript{}, ErrUnavailable
	}
	limit = boundedLimit(limit)
	call, err := s.calls.GetCall(ctx, callID)
	if errors.Is(err, voicecalldomain.ErrCallNotFound) {
		return Transcript{}, ErrCallNotFound
	}
	if err != nil {
		return Transcript{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if call.ID != callID || !validStatus(call.Status) {
		return Transcript{}, ErrUnavailable
	}
	rows, err := s.transcript.ListFinalTurns(ctx, callID, limit)
	if err != nil {
		return Transcript{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	turns := make([]Turn, 0, min(len(rows), limit))
	for _, row := range rows {
		if row.CallID != callID || row.State != "final" || (row.Role != "lead" && row.Role != "agent") {
			continue
		}
		turns = append(turns, Turn{Sequence: row.Sequence, Role: row.Role, Text: row.Text, CreatedAt: row.CreatedAt})
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Sequence < turns[j].Sequence })
	if len(turns) > limit {
		turns = turns[:limit]
	}
	return Transcript{CallID: call.ID, Status: call.Status, Turns: turns}, nil
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

func validStatus(status string) bool {
	switch status {
	case "dialing", "ringing", "connected", "completed", "busy", "no_answer", "failed", "canceled":
		return true
	default:
		return false
	}
}
