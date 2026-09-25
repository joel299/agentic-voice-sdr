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
	ErrNilDependency       = errors.New("bridge: nil dependency")
	ErrFormatIncompatible  = errors.New("bridge: incompatible audio format")
	ErrResponseInterrupted = errors.New("bridge: Gemini response interrupted")
	ErrProviderAPI         = errors.New("bridge: Gemini API error")
	ErrReceiveFailed       = errors.New("bridge: Gemini receive failed")
	ErrSessionClosed       = errors.New("bridge: Gemini session closed")
	ErrAudioOutputFailed   = errors.New("bridge: Gemini audio output failed")
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

type ResponseLifecycle interface {
	CaptureActive() ResponseTurnLease
}

type ResponseTurnLease interface {
	ModelAudioAuthorized() bool
	Complete(context.Context) error
	Fail(context.Context, error) error
}

type Bridge struct {
	input     AudioReader
	output    AudioWriter
	gemini    GeminiSession
	handler   EventHandler
	lifecycle ResponseLifecycle
}

func New(input AudioReader, output AudioWriter, gemini GeminiSession, handler EventHandler, lifecycle ...ResponseLifecycle) *Bridge {
	var responseLifecycle ResponseLifecycle
	if len(lifecycle) > 0 {
		responseLifecycle = lifecycle[0]
	}
	return &Bridge{input: input, output: output, gemini: gemini, handler: handler, lifecycle: responseLifecycle}
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
	type turnState struct {
		mu        sync.Mutex
		sent      uint64
		completed uint64
		progress  chan struct{}
	}
	turns := &turnState{progress: make(chan struct{})}
	markAudioSent := func() {
		turns.mu.Lock()
		turns.sent++
		turns.mu.Unlock()
	}
	markTurnComplete := func() {
		turns.mu.Lock()
		turns.completed = turns.sent
		close(turns.progress)
		turns.progress = make(chan struct{})
		turns.mu.Unlock()
	}
	waitForCompletedInput := func(ctx context.Context) bool {
		for {
			turns.mu.Lock()
			if turns.completed >= turns.sent {
				turns.mu.Unlock()
				return true
			}
			progress := turns.progress
			turns.mu.Unlock()
			select {
			case <-progress:
			case <-ctx.Done():
				return false
			}
		}
	}
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
			if waitForCompletedInput(ctx) {
				internalEnded.Store(true)
				cancel()
			}
		case <-ctx.Done():
		}
	}()

	go func() {
		err := b.runIngress(ctx, markAudioSent)
		if err == nil {
			close(inputDone)
		}
		results <- result{err: err}
	}()
	go func() { results <- result{err: b.runEgress(ctx, markTurnComplete), egress: true} }()

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

func (b *Bridge) runIngress(ctx context.Context, markAudioSent func()) error {
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
			markAudioSent()
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

type providerTurnDisposition uint8

const (
	providerTurnIdle providerTurnDisposition = iota
	providerTurnDenied
	providerTurnOwned
)

func (b *Bridge) runEgress(ctx context.Context, markTurnComplete func()) error {
	disposition := providerTurnIdle
	var lease ResponseTurnLease
	beginProviderTurn := func() {
		if disposition != providerTurnIdle {
			return
		}
		if b.lifecycle == nil {
			disposition = providerTurnDenied
			return
		}
		lease = b.lifecycle.CaptureActive()
		if lease == nil {
			disposition = providerTurnDenied
			return
		}
		disposition = providerTurnOwned
	}
	resetProviderTurn := func() {
		disposition = providerTurnIdle
		lease = nil
	}
	failProviderTurn := func(reason error) {
		if disposition == providerTurnOwned {
			_ = lease.Fail(ctx, reason)
		}
		resetProviderTurn()
	}
	completeProviderTurn := func() error {
		if disposition != providerTurnOwned {
			resetProviderTurn()
			return nil
		}
		err := lease.Complete(ctx)
		resetProviderTurn()
		return err
	}

	for {
		event, err := b.gemini.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failProviderTurn(ErrReceiveFailed)
			return err
		}
		if event.Kind == geminilive.EventAudio {
			if event.AudioMimeType != "audio/pcm;rate=24000" {
				return fmt.Errorf("%w: Gemini %q cannot be sent as AudioSocket SLIN24", ErrFormatIncompatible, event.AudioMimeType)
			}
			beginProviderTurn()
			if len(event.Audio) == 0 || disposition != providerTurnOwned || !lease.ModelAudioAuthorized() {
				continue
			}
			if err := b.output.WriteFrame(audiosocket.Frame{Type: audiosocket.TypeSlin24, Payload: event.Audio}); err != nil {
				failProviderTurn(ErrAudioOutputFailed)
				return err
			}
			continue
		}
		if event.Kind == geminilive.EventOutputTranscription || event.Kind == geminilive.EventToolCall {
			beginProviderTurn()
		}
		if event.Kind == geminilive.EventTurnComplete {
			if err := completeProviderTurn(); err != nil {
				return err
			}
		}
		if event.Kind == geminilive.EventInterrupted {
			failProviderTurn(ErrResponseInterrupted)
		}
		if event.Kind == geminilive.EventAPIError {
			failProviderTurn(ErrProviderAPI)
		}
		if event.Kind == geminilive.EventClosed {
			failProviderTurn(ErrSessionClosed)
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
			// TurnComplete closes only the current model turn. The input
			// watcher ends the session only after the latest sent input is
			// covered, so a stale completion can never stop Receive.
		}
	}
}

func closeIfPossible(value any) {
	if closer, ok := value.(io.Closer); ok {
		_ = closer.Close()
	}
}
