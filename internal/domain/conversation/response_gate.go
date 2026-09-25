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
	ErrResponseAlreadyReserved   = errors.New("response already reserved")
	ErrResponseCycleActive       = errors.New("response cycle active")
	ErrResponseNotReserved       = errors.New("response not reserved")
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
	ResponseReserved   ResponseState = "reserved"
	ResponseAuthorized ResponseState = "authorized"
	ResponseStarted    ResponseState = "started"
	ResponseCompleted  ResponseState = "completed"
	ResponseFailed     ResponseState = "failed"
)

// ResponseReservation is the minimal result of a successful pre-runtime claim.
type ResponseReservation struct {
	Key   ResponseKey
	State ResponseState
}

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

// Reserve claims the latest finalized lead turn before runtime side effects.
func (g *ResponseGate) Reserve(state *ConversationState, sourceTurnID string) (ResponseReservation, error) {
	key, err := responseSourceKey(state, sourceTurnID)
	if err != nil {
		return ResponseReservation{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureCyclesLocked()
	if existing, exists := g.cycles[key]; exists {
		if existing == ResponseReserved {
			return ResponseReservation{}, ErrResponseAlreadyReserved
		}
		return ResponseReservation{}, ErrResponseAlreadyAuthorized
	}
	if g.hasActiveCycleLocked(key.ConversationID, key) {
		return ResponseReservation{}, ErrResponseCycleActive
	}
	g.cycles[key] = ResponseReserved
	return ResponseReservation{Key: key, State: ResponseReserved}, nil
}

// Authorize validates that sourceTurnID is the latest finalized lead turn,
// validates the directive, and authorizes the response exactly once.
func (g *ResponseGate) Authorize(state *ConversationState, sourceTurnID string, directive TurnDirective) (ResponseAuthorization, error) {
	key, err := responseSourceKey(state, sourceTurnID)
	if err != nil {
		return ResponseAuthorization{}, err
	}
	if err := directive.Validate(); err != nil {
		return ResponseAuthorization{}, fmt.Errorf("%w: %v", ErrInvalidResponseDirective, err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureCyclesLocked()
	if _, exists := g.cycles[key]; exists {
		return ResponseAuthorization{}, ErrResponseAlreadyAuthorized
	}
	if g.hasActiveCycleLocked(key.ConversationID, key) {
		return ResponseAuthorization{}, ErrResponseCycleActive
	}
	g.cycles[key] = ResponseAuthorized
	return ResponseAuthorization{Key: key, State: ResponseAuthorized}, nil
}

// AuthorizeReserved promotes a reservation only after a valid directive exists.
func (g *ResponseGate) AuthorizeReserved(key ResponseKey, directive TurnDirective) (ResponseAuthorization, error) {
	if err := key.validate(); err != nil {
		return ResponseAuthorization{}, err
	}
	if err := directive.Validate(); err != nil {
		return ResponseAuthorization{}, fmt.Errorf("%w: %v", ErrInvalidResponseDirective, err)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.cycles[key] {
	case ResponseReserved:
		g.cycles[key] = ResponseAuthorized
		return ResponseAuthorization{Key: key, State: ResponseAuthorized}, nil
	case ResponseAuthorized, ResponseStarted, ResponseCompleted, ResponseFailed:
		return ResponseAuthorization{}, ErrResponseAlreadyAuthorized
	default:
		return ResponseAuthorization{}, ErrResponseNotReserved
	}
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
	case ResponseAuthorized, ResponseReserved:
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
	case ResponseReserved, ResponseAuthorized, ResponseStarted:
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

func (g *ResponseGate) ensureCyclesLocked() {
	if g.cycles == nil {
		g.cycles = make(map[ResponseKey]ResponseState)
	}
}

func (g *ResponseGate) hasActiveCycleLocked(conversationID string, excluded ResponseKey) bool {
	for key, state := range g.cycles {
		if key == excluded || key.ConversationID != conversationID {
			continue
		}
		switch state {
		case ResponseReserved, ResponseAuthorized, ResponseStarted:
			return true
		}
	}
	return false
}

func responseSourceKey(state *ConversationState, sourceTurnID string) (ResponseKey, error) {
	if state == nil || state.ID() == "" || sourceTurnID == "" {
		return ResponseKey{}, ErrInvalidResponseSource
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
		return ResponseKey{}, ErrInvalidResponseSource
	}
	if latestFinalLead == nil || latestFinalLead.ID != sourceTurnID {
		return ResponseKey{}, ErrStaleResponseTurn
	}
	return ResponseKey{ConversationID: state.ID(), SourceTurnID: sourceTurnID}, nil
}

func (key ResponseKey) validate() error {
	if key.ConversationID == "" || key.SourceTurnID == "" {
		return ErrInvalidResponseKey
	}
	return nil
}
