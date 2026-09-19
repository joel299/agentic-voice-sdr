package call

import (
	"fmt"
	"time"
)

// State identifies the lifecycle state of a call session.
type State string

const (
	StateCreated    State = "CREATED"
	StateDialing    State = "DIALING"
	StateRinging    State = "RINGING"
	StateConnected  State = "CONNECTED"
	StateConversing State = "CONVERSING"
	StateEnding     State = "ENDING"
	StateCompleted  State = "COMPLETED"
	StateNoAnswer   State = "NO_ANSWER"
	StateBusy       State = "BUSY"
	StateVoicemail  State = "VOICEMAIL"
	StateFailed     State = "FAILED"
	StateCanceled   State = "CANCELED"

	Created    = StateCreated
	Dialing    = StateDialing
	Ringing    = StateRinging
	Connected  = StateConnected
	Conversing = StateConversing
	Ending     = StateEnding
	Completed  = StateCompleted
	NoAnswer   = StateNoAnswer
	Busy       = StateBusy
	Voicemail  = StateVoicemail
	Failed     = StateFailed
	Canceled   = StateCanceled
)

// StateTransition records a valid state change.
type StateTransition struct {
	From       State
	To         State
	OccurredAt time.Time
}

// InvalidTransitionError reports an attempted invalid state change.
type InvalidTransitionError struct {
	From State
	To   State
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid call session transition: %s -> %s", e.From, e.To)
}

// CallSession is a pure, in-memory call lifecycle state machine.
type CallSession struct {
	state State
	clock func() time.Time
}

// NewCallSession creates a session in CREATED. A clock can be supplied for
// deterministic event timestamps; the default uses time.Now.
func NewCallSession(clock ...func() time.Time) *CallSession {
	now := time.Now
	if len(clock) > 0 && clock[0] != nil {
		now = clock[0]
	}
	return &CallSession{state: StateCreated, clock: now}
}

// State returns the current lifecycle state.
func (s *CallSession) State() State {
	return s.state
}

// CurrentState is an explicit alias for State.
func (s *CallSession) CurrentState() State {
	return s.State()
}

// Transition applies a valid state change and returns its domain event.
func (s *CallSession) Transition(to State) (StateTransition, error) {
	if !validTransition(s.state, to) {
		return StateTransition{}, &InvalidTransitionError{From: s.state, To: to}
	}
	event := StateTransition{From: s.state, To: to, OccurredAt: s.clock()}
	s.state = to
	return event, nil
}

func validTransition(from, to State) bool {
	switch from {
	case StateCreated:
		return to == StateDialing || to == StateCanceled || to == StateFailed
	case StateDialing:
		return to == StateRinging || to == StateNoAnswer || to == StateBusy || to == StateFailed || to == StateCanceled
	case StateRinging:
		return to == StateConnected || to == StateNoAnswer || to == StateBusy || to == StateVoicemail || to == StateFailed || to == StateCanceled
	case StateConnected:
		return to == StateConversing || to == StateEnding || to == StateFailed || to == StateCanceled
	case StateConversing:
		return to == StateEnding || to == StateFailed || to == StateCanceled
	case StateEnding:
		return to == StateCompleted || to == StateFailed
	default:
		return false
	}
}
