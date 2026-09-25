// Package bridge connects normalized AudioSocket frames to a Gemini Live
// session. It deliberately does not adapt codecs or sample rates.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

var (
	ErrNilDependency      = errors.New("bridge: nil dependency")
	ErrFormatIncompatible = errors.New("bridge: incompatible audio format")
)

type AudioReader interface {
	ReadFrame() (audiosocket.Frame, error)
}

type AudioWriter interface {
	WriteFrame(audiosocket.Frame) error
}

type GeminiSession interface {
	SendAudio(context.Context, []byte) error
	EndAudio(context.Context) error
	Receive(context.Context) (geminilive.Event, error)
	Close() error
}

type EventHandler func(context.Context, geminilive.Event) error

type Bridge struct {
	input   AudioReader
	output  AudioWriter
	gemini  GeminiSession
	handler EventHandler
}

func New(input AudioReader, output AudioWriter, gemini GeminiSession, handler EventHandler) *Bridge {
	return &Bridge{input: input, output: output, gemini: gemini, handler: handler}
}

// Run connects both realtime directions until the session completes, one side
// disconnects, or ctx is canceled. Audio is passed through only when its wire
// format already matches the provider contract: SLIN16 ingress and SLIN24
// egress. No unbounded queue is used; each frame/event is handled synchronously
// and cancellation reaches every blocking operation owned by the bridge.
func (b *Bridge) Run(ctx context.Context) error {
	if b == nil || b.input == nil || b.output == nil || b.gemini == nil {
		return ErrNilDependency
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	inputDone := make(chan struct{})
	turnCompleteSeen := make(chan struct{})
	var turnCompleteOnce sync.Once
	markTurnComplete := func() { turnCompleteOnce.Do(func() { close(turnCompleteSeen) }) }
	type result struct {
		err    error
		egress bool
	}
	results := make(chan result, 2)
	var internalEnded atomic.Bool
	var closeOnce sync.Once
	closeSides := func() {
		closeOnce.Do(func() {
			closeIfPossible(b.input)
			closeIfPossible(b.output)
			_ = b.gemini.Close()
		})
	}
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeSides()
		case <-watchDone:
		}
	}()
	defer close(watchDone)
	go func() {
		select {
		case <-inputDone:
			select {
			case <-turnCompleteSeen:
				internalEnded.Store(true)
				cancel()
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()

	go func() {
		err := b.runIngress(ctx)
		if err == nil {
			close(inputDone)
		}
		results <- result{err: err}
	}()
	go func() { results <- result{err: b.runEgress(ctx, inputDone, markTurnComplete), egress: true} }()

	var firstErr error
	for i := 0; i < 2; i++ {
		res := <-results
		err := res.err
		if res.egress && err == nil {
			internalEnded.Store(true)
			cancel()
			closeSides()
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			firstErr = err
			cancel()
			closeSides()
		}
	}
	closeSides()
	if firstErr != nil {
		return firstErr
	}
	if internalEnded.Load() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (b *Bridge) runIngress(ctx context.Context) error {
	for {
		frame, err := b.input.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return b.gemini.EndAudio(ctx)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch frame.Type {
		case audiosocket.TypeSlin16:
			if len(frame.Payload) == 0 {
				continue
			}
			if err := b.gemini.SendAudio(ctx, frame.Payload); err != nil {
				return err
			}
		case audiosocket.TypeHangup:
			return b.gemini.EndAudio(ctx)
		case audiosocket.TypeID, audiosocket.TypeDTMF:
			// AudioSocket control frames do not belong in the Gemini audio path.
			continue
		default:
			return fmt.Errorf("%w: AudioSocket %s cannot be sent as Gemini PCM16", ErrFormatIncompatible, frame.Type)
		}
	}
}

func (b *Bridge) runEgress(ctx context.Context, inputDone <-chan struct{}, markTurnComplete func()) error {
	for {
		event, err := b.gemini.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if event.Kind == geminilive.EventAudio {
			if event.AudioMimeType != "audio/pcm;rate=24000" {
				return fmt.Errorf("%w: Gemini %q cannot be sent as AudioSocket SLIN24", ErrFormatIncompatible, event.AudioMimeType)
			}
			if len(event.Audio) > 0 {
				if err := b.output.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: event.Audio}); err != nil {
					return err
				}
			}
			continue
		}
		if b.handler != nil {
			if err := b.handler(ctx, event); err != nil {
				return err
			}
		}
		if event.Kind == geminilive.EventClosed {
			return nil
		}
		if event.Kind == geminilive.EventTurnComplete {
			markTurnComplete()
			select {
			case <-inputDone:
				return nil
			default:
				// turnComplete closes only the current model turn. Keep
				// receiving while the caller's AudioSocket remains open.
			}
		}
	}
}

func closeIfPossible(value any) {
	if closer, ok := value.(io.Closer); ok {
		_ = closer.Close()
	}
}
