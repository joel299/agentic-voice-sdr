package conversation

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidResponseSource     = errors.New("invalid response source")
	ErrStaleResponseTurn         = errors.New("stale response turn")
	ErrInvalidResponseDirective  = errors.New("invalid response directive")
	ErrInvalidResponseKey        = errors.New("invalid response key")
	ErrResponseAlreadyAuthorized = errors.New("response already authorized")
	ErrResponseCycleActive       = errors.New("response cycle active")
	ErrResponseNotAuthorized     = errors.New("response not authorized")
	ErrResponseAlreadyStarted    = errors.New("response already started")
	ErrResponseNotStarted        = errors.New("response not started")
	ErrResponseAlreadyCompleted  = errors.New("response already completed")
	ErrResponseAlreadyFailed     = errors.New("response already failed")
)

// ResponseKey identifies one response opportunity by its conversation and the
// finalized lead turn that originated it.
type ResponseKey struct {
	ConversationID string
	SourceTurnID   string
}

// ResponseState is the lifecycle state of one response opportunity.
type ResponseState string

const (
	ResponseIdle       ResponseState = "idle"
	ResponseAuthorized ResponseState = "authorized"
	ResponseStarted    ResponseState = "started"
	ResponseCompleted  ResponseState = "completed"
	ResponseFailed     ResponseState = "failed"
)

// ResponseAuthorization is the minimal result of a successful authorization.
type ResponseAuthorization struct {
	Key   ResponseKey
	State ResponseState
}

// ResponseGate owns response lifecycle transitions in memory. It stores only
// response keys and lifecycle state; source content remains in ConversationState.
type ResponseGate struct {
	mu     sync.Mutex
	cycles map[ResponseKey]ResponseState
}

func NewResponseGate() *ResponseGate {
	return &ResponseGate{cycles: make(map[ResponseKey]ResponseState)}
}

// Authorize validates that sourceTurnID is the latest finalized lead turn,
// validates the directive, and authorizes the response exactly once.
func (g *ResponseGate) Authorize(state *ConversationState, sourceTurnID string, directive TurnDirective) (ResponseAuthorization, error) {
	if state == nil || state.ID() == "" || sourceTurnID == "" {
		return ResponseAuthorization{}, ErrInvalidResponseSource
	}
	if err := directive.Validate(); err != nil {
		return ResponseAuthorization{}, fmt.Errorf("%w: %v", ErrInvalidResponseDirective, err)
	}

	turns := state.Turns()
	var source *Turn
	var latestFinalLead *Turn
	for i := range turns {
		turn := &turns[i]
		if turn.Role == RoleLead && turn.Transcript == TranscriptFinal {
			latestFinalLead = turn
		}
		if turn.ID == sourceTurnID {
			source = turn
		}
	}
	if source == nil || source.Role != RoleLead || source.Transcript != TranscriptFinal {
		return ResponseAuthorization{}, ErrInvalidResponseSource
	}
	if latestFinalLead == nil || latestFinalLead.ID != sourceTurnID {
		return ResponseAuthorization{}, ErrStaleResponseTurn
	}

	key := ResponseKey{ConversationID: state.ID(), SourceTurnID: sourceTurnID}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cycles == nil {
		g.cycles = make(map[ResponseKey]ResponseState)
	}
	if _, exists := g.cycles[key]; exists {
		return ResponseAuthorization{}, ErrResponseAlreadyAuthorized
	}
	for existingKey, responseState := range g.cycles {
		if existingKey.ConversationID != key.ConversationID || existingKey == key {
			continue
		}
		if responseState == ResponseAuthorized || responseState == ResponseStarted {
			return ResponseAuthorization{}, ErrResponseCycleActive
		}
	}
	g.cycles[key] = ResponseAuthorized
	return ResponseAuthorization{Key: key, State: ResponseAuthorized}, nil
}

func (g *ResponseGate) Start(key ResponseKey) error {
	if err := key.validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.cycles[key] {
	case ResponseAuthorized:
		g.cycles[key] = ResponseStarted
		return nil
	case ResponseStarted:
		return ErrResponseAlreadyStarted
	case ResponseCompleted:
		return ErrResponseAlreadyCompleted
	case ResponseFailed:
		return ErrResponseAlreadyFailed
	default:
		return ErrResponseNotAuthorized
	}
}

func (g *ResponseGate) Complete(key ResponseKey) error {
	if err := key.validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.cycles[key] {
	case ResponseStarted:
		g.cycles[key] = ResponseCompleted
		return nil
	case ResponseAuthorized:
		return ErrResponseNotStarted
	case ResponseCompleted:
		return ErrResponseAlreadyCompleted
	case ResponseFailed:
		return ErrResponseAlreadyFailed
	default:
		return ErrResponseNotAuthorized
	}
}

func (g *ResponseGate) Fail(key ResponseKey) error {
	if err := key.validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.cycles[key] {
	case ResponseAuthorized, ResponseStarted:
		g.cycles[key] = ResponseFailed
		return nil
	case ResponseFailed:
		return ErrResponseAlreadyFailed
	case ResponseCompleted:
		return ErrResponseAlreadyCompleted
	default:
		return ErrResponseNotAuthorized
	}
}

func (key ResponseKey) validate() error {
	if key.ConversationID == "" || key.SourceTurnID == "" {
		return ErrInvalidResponseKey
	}
	return nil
}
