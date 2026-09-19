package audiosocket

import "errors"

var (
	// ErrInvalidHeader indicates the input is shorter than HeaderSize.
	ErrInvalidHeader = errors.New("audiosocket: invalid header")

	// ErrTruncatedPayload indicates the declared payload length exceeds available bytes.
	ErrTruncatedPayload = errors.New("audiosocket: truncated payload")

	// ErrUnknownType indicates an unrecognized frame type (reject policy).
	ErrUnknownType = errors.New("audiosocket: unknown frame type")

	// ErrPayloadTooLarge indicates payload length exceeds MaxPayloadSize.
	ErrPayloadTooLarge = errors.New("audiosocket: payload too large")
)
