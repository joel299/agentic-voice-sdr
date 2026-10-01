package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

// TranscriptHandler is the boundary between the input transcription session
// and the turnloop. It intentionally accepts TranscriptEvent, not Event.
type TranscriptHandler interface {
	HandleTranscript(context.Context, geminilive.TranscriptEvent) error
}

type contextAudioReader interface {
	ReadFrameContext(context.Context) (audiosocket.Frame, error)
}

type contextAudioWriter interface {
	WriteFrameContext(context.Context, audiosocket.Frame) error
}

// StageObserver receives fixed, non-sensitive runtime milestones. It must not
// be used to report provider payloads, transcript text, or raw errors.
type StageObserver func(stage, outcome string)

// StageError identifies the bridge boundary that returned an error without
// including provider error text in its message. The cause remains available
// to internal errors.Is/errors.As checks.
type StageError struct {
	Stage string
	Cause error
}

func (e *StageError) Error() string   { return "bridge stage failed: " + e.Stage }
func (e *StageError) Unwrap() error   { return e.Cause }
func (e *StageError) AIStage() string { return e.Stage }

func stageError(stage string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var staged interface{ AIStage() string }
	if errors.As(err, &staged) {
		return err
	}
	return &StageError{Stage: stage, Cause: err}
}

// SplitBridge connects independent input-transcription and controlled-response
// sessions. The two sessions have separate receive owners and capabilities.
type SplitBridge struct {
	input       AudioReader
	output      AudioWriter
	transcriber geminilive.InputTranscriberSession
	responder   geminilive.ControlledResponseSession
	transcript  TranscriptHandler
	events      EventHandler
	lifecycle   ResponseLifecycle
	observe     StageObserver
}

func NewSplit(input AudioReader, output AudioWriter, transcriber geminilive.InputTranscriberSession, responder geminilive.ControlledResponseSession, transcript TranscriptHandler, events EventHandler, lifecycle ...ResponseLifecycle) *SplitBridge {
	var lc ResponseLifecycle
	if len(lifecycle) > 0 {
		lc = lifecycle[0]
	}
	return &SplitBridge{input: input, output: output, transcriber: transcriber, responder: responder, transcript: transcript, events: events, lifecycle: lc}
}

// SetStageObserver installs call-scoped, fixed-name runtime telemetry before
// Run. Callers should set it before starting the bridge.
func (b *SplitBridge) SetStageObserver(observer StageObserver) {
	if b != nil {
		b.observe = observer
	}
}

func (b *SplitBridge) observeStage(stage, outcome string) {
	if b.observe != nil {
		b.observe(stage, outcome)
	}
}

// Run owns exactly one Receive loop for each provider session. PCM is sent
// only to transcriber; response output is read only from responder.
func (b *SplitBridge) Run(ctx context.Context) error {
	if b == nil || b.input == nil || b.output == nil || b.transcriber == nil || b.responder == nil || b.transcript == nil {
		return ErrNilDependency
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	closeAll := func() {
		// Telephony owns its transport lifetime. A provider failure or a caller
		// cancellation of this AI bridge must never close the underlying call.
		once.Do(func() { _ = b.transcriber.Close(); _ = b.responder.Close() })
	}
	type result struct {
		err      error
		response bool
		input    bool
	}
	done := make(chan result, 3)
	go func() { done <- result{err: b.runSplitIngress(ctx), input: true} }()
	go func() { done <- result{err: b.runSplitTranscripts(ctx)} }()
	go func() { done <- result{err: b.runSplitResponses(ctx), response: true} }()

	var first error
	for i := 0; i < 3; i++ {
		res := <-done
		err := res.err
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && first == nil {
			first = err
			cancel()
			closeAll()
		}
		if res.input && err == nil {
			if cancels, ok := b.input.(interface{ CancelOnInputEnd() bool }); ok && cancels.CancelOnInputEnd() {
				cancel()
				closeAll()
			}
		}
		if res.response && err == nil {
			cancel()
			closeAll()
		}
	}
	closeAll()
	if first != nil {
		return first
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (b *SplitBridge) runSplitIngress(ctx context.Context) error {
	for {
		frame, err := readFrame(ctx, b.input)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return stageError("input_transcription_send", b.transcriber.EndAudio(ctx))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return stageError("media_ingress", err)
		}
		switch frame.Type {
		case audiosocket.TypeSlin16:
			if len(frame.Payload) == 0 {
				continue
			}
			b.observeStage("media_ingress", "frame_received")
			if err := b.transcriber.SendAudio(ctx, frame.Payload); err != nil {
				return stageError("input_transcription_send", err)
			}
			b.observeStage("input_transcription_send", "audio_sent")
		case audiosocket.TypeHangup:
			return stageError("input_transcription_send", b.transcriber.EndAudio(ctx))
		case audiosocket.TypeID, audiosocket.TypeDTMF:
			continue
		default:
			return stageError("media_ingress", fmt.Errorf("%w: AudioSocket %s cannot be sent as Gemini PCM16", ErrFormatIncompatible, frame.Type))
		}
	}
}

func (b *SplitBridge) runSplitTranscripts(ctx context.Context) error {
	for {
		event, err := b.transcriber.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return stageError("input_transcription_receive", err)
		}
		b.observeStage("input_transcription_receive", string(event.State))
		if event.State == geminilive.TranscriptFinal {
			b.observeStage("first_final_transcription", "received")
		}
		if err := b.transcript.HandleTranscript(ctx, event); err != nil {
			return stageError("input_transcription_handler", err)
		}
		if event.State == geminilive.TranscriptFinal {
			b.observeStage("input_transcription_handler", "completed")
		}
	}
}

func (b *SplitBridge) runSplitResponses(ctx context.Context) error {
	disposition := providerTurnIdle
	var lease ResponseTurnLease
	begin := func() {
		if disposition != providerTurnIdle {
			return
		}
		if b.lifecycle == nil {
			disposition = providerTurnDenied
			return
		}
		lease = b.lifecycle.CaptureActive()
		if lease == nil || !lease.ModelAudioAuthorized() {
			lease = nil
			disposition = providerTurnDenied
			return
		}
		disposition = providerTurnOwned
	}
	reset := func() { disposition = providerTurnIdle; lease = nil }
	fail := func(reason error) {
		if disposition == providerTurnOwned {
			_ = lease.Fail(ctx, reason)
		}
		reset()
	}
	failSession := func(reason error) {
		if disposition == providerTurnOwned {
			_ = lease.Fail(ctx, reason)
		} else if b.lifecycle != nil {
			_ = b.lifecycle.FailActive(ctx, reason)
		}
		reset()
	}
	complete := func() error {
		if disposition != providerTurnOwned {
			reset()
			return nil
		}
		err := lease.Complete(ctx)
		reset()
		return err
	}

	for {
		event, err := b.responder.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failSession(ErrReceiveFailed)
			return stageError("gemini_response_receive", err)
		}
		if event.Kind == geminilive.EventOutputTranscription {
			b.observeStage("gemini_output_transcription", "received")
		}
		if event.Kind == geminilive.EventGenerationComplete {
			b.observeStage("generation_complete", "received")
		}
		if event.Kind == geminilive.EventTurnComplete || event.TurnComplete {
			b.observeStage("turn_complete", "received")
		}
		if event.Kind == geminilive.EventAudio {
			b.observeStage("gemini_audio", "received")
		}
		turnComplete := event.Kind == geminilive.EventTurnComplete || event.TurnComplete
		switch event.Kind {
		case geminilive.EventAudio:
			if event.AudioMimeType != "audio/pcm;rate=24000" {
				fail(ErrFormatIncompatible)
				return stageError("media_egress", fmt.Errorf("%w: Gemini %q cannot be sent as AudioSocket SLIN24", ErrFormatIncompatible, event.AudioMimeType))
			}
			begin()
			if len(event.Audio) > 0 && disposition == providerTurnOwned {
				if err := writeFrame(ctx, b.output, audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: event.Audio}); err != nil {
					fail(ErrAudioOutputFailed)
					return stageError("media_egress", err)
				}
				b.observeStage("media_egress", "audio_written")
			}
		case geminilive.EventOutputTranscription, geminilive.EventToolCall:
			begin()
		case geminilive.EventInterrupted:
			fail(ErrResponseInterrupted)
		case geminilive.EventAPIError:
			failSession(ErrProviderAPI)
			return stageError("gemini_response_receive", ErrProviderAPI)
		case geminilive.EventClosed:
			if disposition == providerTurnOwned {
				if b.events != nil {
					if err := b.events(ctx, event); err != nil {
						fail(ErrResponseTurnIncomplete)
						return stageError("turn_complete", err)
					}
				}
				fail(ErrResponseTurnIncomplete)
				return fmt.Errorf("%w (close status: %s)", ErrResponseTurnIncomplete, safeCloseStatus(event.CloseStatusClass))
			}
			failSession(ErrSessionClosed)
		}
		if b.events != nil {
			if err := b.events(ctx, event); err != nil {
				fail(err)
				return stageError("gemini_response_receive", err)
			}
		}
		if turnComplete {
			if err := complete(); err != nil {
				return stageError("turn_complete", err)
			}
		}
		if event.Kind == geminilive.EventClosed {
			return nil
		}
	}
}

func readFrame(ctx context.Context, reader AudioReader) (audiosocket.Frame, error) {
	if contextual, ok := reader.(contextAudioReader); ok {
		return contextual.ReadFrameContext(ctx)
	}
	return reader.ReadFrame()
}

func writeFrame(ctx context.Context, writer AudioWriter, frame audiosocket.Frame) error {
	if contextual, ok := writer.(contextAudioWriter); ok {
		return contextual.WriteFrameContext(ctx, frame)
	}
	return writer.WriteFrame(frame)
}

func safeCloseStatus(status geminilive.CloseStatusClass) geminilive.CloseStatusClass {
	switch status {
	case geminilive.CloseStatusNormal, geminilive.CloseStatusGoingAway, geminilive.CloseStatusAbnormal, geminilive.CloseStatusOther:
		return status
	default:
		return geminilive.CloseStatusUnknown
	}
}
