// Package control defines provider-neutral telephony control contracts.
package control

import (
	"context"

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

// Provider is the outbound control surface shared by telephony engines.
// Hangup targets the active call; the MVP permits one concurrent call.
type Provider interface {
	RegistrationStatus(context.Context) (RegistrationStatus, error)
	Dial(context.Context, string) (CommandResult, error)
	Hangup(context.Context) (CommandResult, error)
	ListCalls(context.Context) (CommandResult, error)
	Events() <-chan Event
	Close() error
}
