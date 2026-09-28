package voiceflow

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
	"github.com/joel299/agentic-voice-sdr/internal/toolruntime"
	"github.com/joel299/agentic-voice-sdr/internal/turnloop"
	"github.com/joel299/agentic-voice-sdr/internal/turnruntime"
)

var (
	ErrInvalidFalePacoRuntime = errors.New("voiceflow: invalid Fale Paco runtime")
	ErrMissingAudioSocketID   = errors.New("voiceflow: AudioSocket connection did not start with an ID frame")
)

type FalePacoSessionFactory func(context.Context, string) (geminilive.InputTranscriberSession, geminilive.ControlledResponseSession, error)
type FalePacoStateFactory func(string) (*conversation.ConversationState, error)

type FalePacoRuntimeConfig struct {
	AudioSocketAddr string
	Processor       turnloop.TurnProcessor
	Sessions        FalePacoSessionFactory
	State           FalePacoStateFactory
}

type FalePacoRuntime struct {
	server *audiosocket.Server
	nextID atomic.Uint64
}

func NewFalePacoRuntime(cfg FalePacoRuntimeConfig) (*FalePacoRuntime, error) {
	if strings.TrimSpace(cfg.AudioSocketAddr) == "" || cfg.Processor == nil || cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: address, processor, and session factory are required", ErrInvalidFalePacoRuntime)
	}
	if cfg.State == nil {
		cfg.State = conversation.NewConversationState
	}
	runtime := &FalePacoRuntime{}
	runtime.server = audiosocket.NewServer(cfg.AudioSocketAddr, func(ctx context.Context, stream *audiosocket.Stream) error {
		first, err := stream.ReadFrame()
		if err != nil {
			return err
		}
		if first.Type != audiosocket.TypeID {
			return ErrMissingAudioSocketID
		}
		sessionID := sanitizeFalePacoCallID(first.Payload)
		state, err := cfg.State(sessionID)
		if err != nil {
			return err
		}
		transcriber, responder, err := cfg.Sessions(ctx, sessionID)
		if err != nil {
			return err
		}
		gate := conversation.NewResponseGate()
		audio := &falePacoAudio{stream: stream}
		split, err := NewSplitRuntime(audio, audio, transcriber, responder, state, cfg.Processor, gate, nil, nil)
		if err != nil {
			_ = transcriber.Close()
			_ = responder.Close()
			return err
		}
		return split.Run(ctx)
	})
	return runtime, nil
}

func (r *FalePacoRuntime) Listen() error {
	if r == nil || r.server == nil {
		return ErrInvalidFalePacoRuntime
	}
	return r.server.Listen()
}
func (r *FalePacoRuntime) Addr() string {
	if r == nil || r.server == nil || r.server.Addr() == nil {
		return ""
	}
	return r.server.Addr().String()
}
func (r *FalePacoRuntime) Serve(ctx context.Context) error {
	if r == nil || r.server == nil {
		return ErrInvalidFalePacoRuntime
	}
	return r.server.Serve(ctx)
}
func (r *FalePacoRuntime) Shutdown(ctx context.Context) error {
	if r == nil || r.server == nil {
		return nil
	}
	return r.server.Shutdown(ctx)
}

// NewProductionFalePacoRuntime wires the canonical JEV processor and the two
// Gemini roles. It opens provider sessions only after Asterisk connects.
func NewProductionFalePacoRuntime(addr string, geminiConfig geminilive.Config) (*FalePacoRuntime, error) {
	provider, err := openrouterjev.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	jev, err := openrouterjev.New(provider)
	if err != nil {
		return nil, err
	}
	dispatcher := toolruntime.NewDispatcher(tools.NewInMemoryRegistry(), toolruntime.NewExecutorRegistry(nil))
	processor, err := turnruntime.New(jev, dispatcher)
	if err != nil {
		return nil, err
	}
	sessions := func(ctx context.Context, _ string) (geminilive.InputTranscriberSession, geminilive.ControlledResponseSession, error) {
		input, err := geminilive.ConnectInputTranscriber(ctx, geminiConfig)
		if err != nil {
			return nil, nil, err
		}
		response, err := geminilive.ConnectControlledResponse(ctx, geminiConfig)
		if err != nil {
			_ = input.Close()
			return nil, nil, err
		}
		return input, response, nil
	}
	return NewFalePacoRuntime(FalePacoRuntimeConfig{AudioSocketAddr: addr, Processor: processor, Sessions: sessions})
}

func sanitizeFalePacoCallID(payload []byte) string {
	if len(payload) == 16 {
		return "call-" + hex.EncodeToString(payload)
	}
	return fmt.Sprintf("call-%08x", fallbackCallID.Add(1))
}

var fallbackCallID atomic.Uint64

type falePacoAudio struct{ stream *audiosocket.Stream }

func (a *falePacoAudio) ReadFrame() (audiosocket.Frame, error) {
	if a == nil || a.stream == nil {
		return audiosocket.Frame{}, io.ErrClosedPipe
	}
	return a.stream.ReadFrame()
}
func (a *falePacoAudio) WriteFrame(f audiosocket.Frame) error {
	if a == nil || a.stream == nil {
		return io.ErrClosedPipe
	}
	return a.stream.WriteFrame(f)
}
func (a *falePacoAudio) CancelOnInputEnd() bool { return true }

func (a *falePacoAudio) Close() error {
	if a == nil || a.stream == nil {
		return nil
	}
	return a.stream.Close()
}

var _ bridge.AudioReader = (*falePacoAudio)(nil)
var _ bridge.AudioWriter = (*falePacoAudio)(nil)
