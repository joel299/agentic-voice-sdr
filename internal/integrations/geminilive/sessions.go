package geminilive

import (
	"context"
	"errors"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
)

var (
	ErrConcurrentReceiveOwner = errors.New("geminilive: receive already has an owner")
	ErrTranscriptionAPI       = errors.New("geminilive: transcription provider error")
	ErrTranscriptionClosed    = errors.New("geminilive: transcription session closed")
)

// TranscriptEvent contains only recognized phone-input transcription. It has
// no audio or model-response fields by construction.
type TranscriptEvent struct {
	State   InputTranscriptState
	Text    string
	EventID string
	TurnID  string
}

// InputTranscriberSession is the only public capability that accepts raw phone
// audio. Receive exposes only interim/final input transcript events.
type InputTranscriberSession interface {
	SendAudio(context.Context, []byte) error
	EndAudio(context.Context) error
	Receive(context.Context) (TranscriptEvent, error)
	Close() error
}

// ControlledResponseSession accepts only coordinator-issued turn directives
// and exposes response events. It deliberately has no SendAudio capability.
type ControlledResponseSession interface {
	SendControlledTurn(context.Context, string, conversation.TurnDirective) error
	Receive(context.Context) (Event, error)
	Close() error
}

type inputTranscriber struct {
	provider  *providerSession
	sequencer *LeadTurnSequencer
}

func (*inputTranscriber) ownsLeadTurnIdentity() {}
func (s *inputTranscriber) attachLeadTurnSequencer(sequencer *LeadTurnSequencer) {
	if sequencer != nil {
		s.sequencer = sequencer
	}
}

type controlledResponder struct {
	provider *providerSession
}

var (
	_ InputTranscriberSession   = (*inputTranscriber)(nil)
	_ ControlledResponseSession = (*controlledResponder)(nil)
)

// ConnectInputTranscriber opens a dedicated Gemini Live session for phone
// audio and input transcription. Its configured model remains caller-injected
// via Config.Model. Tools are withheld; any model/output events are discarded.
func ConnectInputTranscriber(ctx context.Context, cfg Config) (InputTranscriberSession, error) {
	return ConnectInputTranscriberWithTurnSequencer(ctx, cfg, NewLeadTurnSequencer(0))
}

// ConnectInputTranscriberWithTurnSequencer connects a Gemini input session
// using a call-scoped turn owner. Reuse the same sequencer when reconnecting a
// provider session during the same call.
func ConnectInputTranscriberWithTurnSequencer(ctx context.Context, cfg Config, sequencer *LeadTurnSequencer) (InputTranscriberSession, error) {
	if sequencer == nil {
		sequencer = NewLeadTurnSequencer(0)
	}
	cfg.Tools = nil
	provider, err := connect(ctx, cfg, roleInputTranscription)
	if err != nil {
		return nil, err
	}
	var session InputTranscriberSession = &inputTranscriber{provider: provider, sequencer: sequencer}
	if cfg.VAD.Hybrid {
		session = WithHybridVAD(session, cfg.VAD.SilenceDurationMS)
	}
	return session, nil
}

// ConnectControlledResponse opens a separate Gemini Live session for controlled
// response turns. It receives no microphone capability and is given no tools.
func ConnectControlledResponse(ctx context.Context, cfg Config) (ControlledResponseSession, error) {
	cfg.Tools = nil
	provider, err := connect(ctx, cfg, roleControlledResponse)
	if err != nil {
		return nil, err
	}
	return &controlledResponder{provider: provider}, nil
}

func (s *inputTranscriber) SendAudio(ctx context.Context, pcm16k []byte) error {
	if s == nil || s.provider == nil {
		return ErrNotReady
	}
	return s.provider.SendAudio(ctx, pcm16k)
}

func (s *inputTranscriber) EndAudio(ctx context.Context) error {
	if s == nil || s.provider == nil {
		return ErrNotReady
	}
	return s.provider.EndAudio(ctx)
}

// Receive discards every event except input interim/final transcription. Model
// audio, model text, tool calls, and response lifecycle markers are never
// returned through this phone-input boundary.
func (s *inputTranscriber) Receive(ctx context.Context) (TranscriptEvent, error) {
	if s == nil || s.provider == nil {
		return TranscriptEvent{}, ErrNotReady
	}
	for {
		event, err := s.provider.Receive(ctx)
		if err != nil {
			if errors.Is(err, ErrRemoteClosed) {
				return TranscriptEvent{}, ErrTranscriptionClosed
			}
			return TranscriptEvent{}, err
		}
		switch event.Kind {
		case EventInputTranscription:
			if event.InputTranscriptState == TranscriptInterim || event.InputTranscriptState == TranscriptFinal {
				transcript := TranscriptEvent{State: event.InputTranscriptState, Text: event.Text, EventID: event.EventID}
				return s.sequencer.assign(transcript), nil
			}
		case EventAPIError:
			return TranscriptEvent{}, ErrTranscriptionAPI
		}
		// All non-input-transcription events are intentionally consumed and dropped.
	}
}

func (s *inputTranscriber) Close() error {
	if s == nil || s.provider == nil {
		return nil
	}
	return s.provider.Close()
}

func (s *controlledResponder) SendControlledTurn(ctx context.Context, finalLeadText string, directive conversation.TurnDirective) error {
	if s == nil || s.provider == nil {
		return ErrNotReady
	}
	return s.provider.SendControlledTurn(ctx, finalLeadText, directive)
}

// Receive returns only controlled response lifecycle/output events. Input
// transcript, setup, unknown, and tool-call events are not response output.
func (s *controlledResponder) Receive(ctx context.Context) (Event, error) {
	if s == nil || s.provider == nil {
		return Event{}, ErrNotReady
	}
	for {
		event, err := s.provider.Receive(ctx)
		if err != nil {
			if errors.Is(err, ErrRemoteClosed) {
				var providerErr *Error
				closeClass := CloseStatusUnknown
				transportClass := TransportRemoteClose
				if errors.As(err, &providerErr) {
					closeClass = providerErr.CloseStatusClass
					transportClass = providerErr.TransportClass
				}
				return Event{Kind: EventClosed, CloseStatusClass: closeClass, TransportClass: transportClass}, nil
			}
			return Event{}, err
		}
		switch event.Kind {
		case EventAudio, EventOutputTranscription, EventGenerationComplete, EventTurnComplete, EventGoAway, EventSessionResumption, EventServerStatus, EventInterrupted, EventAPIError, EventClosed:
			return event, nil
		}
	}
}

func (s *controlledResponder) Close() error {
	if s == nil || s.provider == nil {
		return nil
	}
	return s.provider.Close()
}
