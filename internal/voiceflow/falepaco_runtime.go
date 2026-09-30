package voiceflow

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
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
type FalePacoLogger interface {
	Log(event, callID, errorClass string)
	Close() error
}
type FalePacoCallIDResolver func(context.Context, string) (string, error)

type fileFalePacoLogger struct {
	mu     sync.Mutex
	file   *os.File
	logger *log.Logger
}

func NewFalePacoFileLogger(path string) (FalePacoLogger, error) {
	if strings.TrimSpace(path) == "" {
		return noopFalePacoLogger{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &fileFalePacoLogger{file: file, logger: log.New(file, "", log.LstdFlags|log.LUTC)}, nil
}
func (l *fileFalePacoLogger) Log(event, callID, errorClass string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger.Printf("event=%s call_id=%s error_class=%s", event, callID, errorClass)
}
func (l *fileFalePacoLogger) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.file.Close() }

type noopFalePacoLogger struct{}

func (noopFalePacoLogger) Log(string, string, string) {}
func (noopFalePacoLogger) Close() error               { return nil }
func falepacoErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrMissingAudioSocketID) {
		return "invalid_id"
	}
	if errors.Is(err, io.EOF) {
		return "disconnect"
	}
	return "runtime_error"
}

type FalePacoRuntimeConfig struct {
	AudioSocketAddr string
	Processor       turnloop.TurnProcessor
	Sessions        FalePacoSessionFactory
	State           FalePacoStateFactory
	Logger          FalePacoLogger
	Transcripts     voicecalldomain.TranscriptRepository
	ResolveCallID   FalePacoCallIDResolver
}

type FalePacoRuntime struct {
	server *audiosocket.Server
	logger FalePacoLogger
	nextID atomic.Uint64
}

func NewFalePacoRuntime(cfg FalePacoRuntimeConfig) (*FalePacoRuntime, error) {
	if strings.TrimSpace(cfg.AudioSocketAddr) == "" || cfg.Processor == nil || cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: address, processor, and session factory are required", ErrInvalidFalePacoRuntime)
	}
	if (cfg.Transcripts == nil) != (cfg.ResolveCallID == nil) {
		return nil, fmt.Errorf("%w: transcript repository and call ID resolver must be configured together", ErrInvalidFalePacoRuntime)
	}
	if cfg.State == nil {
		cfg.State = conversation.NewConversationState
	}
	if cfg.Logger == nil {
		cfg.Logger = noopFalePacoLogger{}
	}
	runtime := &FalePacoRuntime{logger: cfg.Logger}
	runtime.server = audiosocket.NewServer(cfg.AudioSocketAddr, func(ctx context.Context, stream *audiosocket.Stream) error {
		callID := ""
		cfg.Logger.Log("call_accept", callID, "")
		defer func() { cfg.Logger.Log("call_end", callID, "") }()
		first, err := stream.ReadFrame()
		if err != nil {
			cfg.Logger.Log("audiosocket_disconnect", callID, falepacoErrorClass(err))
			return err
		}
		if first.Type != audiosocket.TypeID {
			return ErrMissingAudioSocketID
		}
		sessionID := sanitizeFalePacoCallID(first.Payload)
		callID = sessionID
		cfg.Logger.Log("call_id", callID, "")
		cfg.Logger.Log("call_start", callID, "")
		cfg.Logger.Log("audiosocket_connect", callID, "")
		cfg.Logger.Log("session_factory_start", callID, "")
		state, err := cfg.State(sessionID)
		if err != nil {
			return err
		}
		transcriber, responder, err := cfg.Sessions(ctx, sessionID)
		if err != nil {
			cfg.Logger.Log("session_factory_failure", callID, falepacoErrorClass(err))
			return err
		}
		cfg.Logger.Log("gemini_input_open", callID, "")
		cfg.Logger.Log("gemini_response_open", callID, "")
		gate := conversation.NewResponseGate()
		audio := &falePacoAudio{stream: stream}
		var split *bridge.SplitBridge
		if cfg.Transcripts != nil {
			if cfg.ResolveCallID == nil {
				_ = transcriber.Close()
				_ = responder.Close()
				return ErrInvalidFalePacoRuntime
			}
			persistentCallID, resolveErr := cfg.ResolveCallID(ctx, sessionID)
			if resolveErr != nil || persistentCallID == "" {
				_ = transcriber.Close()
				_ = responder.Close()
				return ErrInvalidFalePacoRuntime
			}
			split, err = NewSplitRuntimeWithTranscriptPersistence(audio, audio, transcriber, responder, state, cfg.Processor, gate, nil, nil, persistentCallID, cfg.Transcripts)
		} else {
			split, err = NewSplitRuntime(audio, audio, transcriber, responder, state, cfg.Processor, gate, nil, nil)
		}
		if err != nil {
			_ = transcriber.Close()
			_ = responder.Close()
			return err
		}
		cfg.Logger.Log("split_runtime_start", callID, "")
		err = split.Run(ctx)
		cfg.Logger.Log("split_runtime_end", callID, falepacoErrorClass(err))
		cfg.Logger.Log("cleanup", callID, "")
		return err
	})
	runtime.server.SetConnectionErrorHandler(func(err error) { cfg.Logger.Log("error", "", falepacoErrorClass(err)) })
	return runtime, nil
}

func (r *FalePacoRuntime) Listen() error {
	if r == nil || r.server == nil {
		return ErrInvalidFalePacoRuntime
	}
	err := r.server.Listen()
	if err == nil {
		r.logger.Log("listener_start", "", "")
	}
	return err
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
	err := r.server.Shutdown(ctx)
	if err == nil {
		r.logger.Log("listener_stop", "", "")
		_ = r.logger.Close()
	}
	return err
}

// NewProductionFalePacoRuntime wires the canonical JEV processor and the two
// Gemini roles. It opens provider sessions only after Asterisk connects.
func NewProductionFalePacoRuntime(addr string, geminiConfig geminilive.Config) (*FalePacoRuntime, error) {
	return NewProductionFalePacoRuntimeWithLogger(addr, geminiConfig, nil)
}

func NewProductionFalePacoRuntimeWithLogger(addr string, geminiConfig geminilive.Config, logger FalePacoLogger) (*FalePacoRuntime, error) {
	return NewProductionFalePacoRuntimeWithPersistence(addr, geminiConfig, logger, nil, nil)
}

// NewProductionFalePacoRuntimeWithPersistence exposes the durable transcript
// integration boundary. The resolver must map the AudioSocket UUID to an
// existing canonical call record; the runtime deliberately does not guess.
func NewProductionFalePacoRuntimeWithPersistence(addr string, geminiConfig geminilive.Config, logger FalePacoLogger, transcripts voicecalldomain.TranscriptRepository, resolveCallID FalePacoCallIDResolver) (*FalePacoRuntime, error) {
	if (transcripts == nil) != (resolveCallID == nil) {
		return nil, ErrInvalidFalePacoRuntime
	}
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
	return NewFalePacoRuntime(FalePacoRuntimeConfig{AudioSocketAddr: addr, Processor: processor, Sessions: sessions, Logger: logger, Transcripts: transcripts, ResolveCallID: resolveCallID})
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
