// Package control defines provider-neutral telephony control contracts.
package control

import (
	"context"
	"errors"

	callstate "github.com/joel299/agentic-voice-sdr/internal/domain/call"
)

type RegistrationState string

const (
	RegistrationUnknown       RegistrationState = "UNKNOWN"
	RegistrationRegistered    RegistrationState = "REGISTERED"
	RegistrationRegistering   RegistrationState = "REGISTERING"
	RegistrationNotRegistered RegistrationState = "NOT_REGISTERED"
	RegistrationFailed        RegistrationState = "FAILED"
)

type RegistrationStatus struct {
	State  RegistrationState
	Detail string
}

type CallState = callstate.State

const (
	CallStateUnknown   CallState = ""
	CallStateOutgoing  CallState = callstate.StateDialing
	CallStateProgress  CallState = callstate.StateRinging
	CallStateRinging   CallState = callstate.StateRinging
	CallStateConnected CallState = callstate.StateConnected
	CallStateCompleted CallState = callstate.StateCompleted
	CallStateNoAnswer  CallState = callstate.StateNoAnswer
	CallStateBusy      CallState = callstate.StateBusy
	CallStateFailed    CallState = callstate.StateFailed
	CallStateCanceled  CallState = callstate.StateCanceled
)

// Event is a normalized telephony event. Provider-specific names remain in
// Type and Class so callers can preserve detail without parsing wire payloads.
type Event struct {
	Class             string
	Type              string
	State             CallState
	RegistrationState RegistrationState
	CallID            string
	PeerURI           string
	Direction         string
	Param             string
}

type CommandResult struct {
	Token string
	Data  string
	OK    bool
}

type ActiveCall struct {
	ProviderCallID string
	PeerURI        string
	State          CallState
}

type DispatchCertainty uint8

const (
	DispatchNotDispatched DispatchCertainty = iota + 1
	DispatchRejected
	DispatchMaybeDispatched
)

// CommandError describes whether a state-changing command may have reached
// the provider. Cause is retained for internal error matching and diagnostics;
// API boundaries must map it to a safe fixed message.
type CommandError struct {
	Certainty DispatchCertainty
	Cause     error
}

func (e *CommandError) Error() string {
	if e == nil || e.Cause == nil {
		return "telephony command failed"
	}
	return e.Cause.Error()
}

func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func DispatchCertaintyOf(err error) DispatchCertainty {
	var commandErr *CommandError
	if errors.As(err, &commandErr) && commandErr.Certainty != 0 {
		return commandErr.Certainty
	}
	// Unknown provider errors fail closed: assume the command may have been sent.
	return DispatchMaybeDispatched
}

// Provider is the outbound control surface shared by telephony engines.
// Hangup targets the active call; the MVP permits one concurrent call.
type Provider interface {
	RegistrationStatus(context.Context) (RegistrationStatus, error)
	ActiveCalls(context.Context) ([]ActiveCall, error)
	Dial(context.Context, string) (CommandResult, error)
	Hangup(context.Context) (CommandResult, error)
	ListCalls(context.Context) (CommandResult, error)
	Events() <-chan Event
	Close() error
}
