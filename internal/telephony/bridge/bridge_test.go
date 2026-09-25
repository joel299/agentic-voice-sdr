package bridge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/audiosocket"
)

type fakeAudio struct {
	mu       sync.Mutex
	frames   []audiosocket.Frame
	readErr  error
	writes   []audiosocket.Frame
	closed   chan struct{}
	closeOne sync.Once
}

func newFakeAudio(frames []audiosocket.Frame, readErr error) *fakeAudio {
	return &fakeAudio{frames: frames, readErr: readErr, closed: make(chan struct{})}
}
func (f *fakeAudio) ReadFrame() (audiosocket.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) > 0 {
		frame := f.frames[0]
		f.frames = f.frames[1:]
		return frame, nil
	}
	return audiosocket.Frame{}, f.readErr
}
func (f *fakeAudio) WriteFrame(frame audiosocket.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, frame)
	return nil
}
func (f *fakeAudio) Close() error { f.closeOne.Do(func() { close(f.closed) }); return nil }

func (f *fakeAudio) written() []audiosocket.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audiosocket.Frame(nil), f.writes...)
}

type fakeGemini struct {
	mu       sync.Mutex
	sent     [][]byte
	events   []geminilive.Event
	sendErr  error
	recvErr  error
	closed   chan struct{}
	closeOne sync.Once
}

func (f *fakeGemini) SendAudio(_ context.Context, audio []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, append([]byte(nil), audio...))
	return f.sendErr
}

var _ = (*fakeGemini)(nil)

// Receive is expected to return provider events without executing tool calls.
func (f *fakeGemini) Receive(ctx context.Context) (geminilive.Event, error) {
	f.mu.Lock()
	if len(f.events) > 0 {
		event := f.events[0]
		f.events = f.events[1:]
		f.mu.Unlock()
		return event, nil
	}
	err := f.recvErr
	f.mu.Unlock()
	if err != nil {
		return geminilive.Event{}, err
	}
	<-ctx.Done()
	return geminilive.Event{}, ctx.Err()
}
func (f *fakeGemini) EndAudio(context.Context) error { return nil }
func (f *fakeGemini) Close() error                   { f.closeOne.Do(func() { close(f.closed) }); return nil }

func TestBridgeRoutesAudioBothDirections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	audio := newFakeAudio([]audiosocket.Frame{{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2}}}, io.EOF)
	gemini := &fakeGemini{events: []geminilive.Event{
		{Kind: geminilive.EventAudio, Audio: []byte{3, 4}, AudioMimeType: "audio/pcm;rate=24000"},
		{Kind: geminilive.EventTurnComplete},
	}, closed: make(chan struct{})}
	var got []geminilive.Event
	var mu sync.Mutex
	b := New(audio, audio, gemini, func(_ context.Context, event geminilive.Event) error {
		mu.Lock()
		got = append(got, event)
		mu.Unlock()
		return nil
	})
	if err := b.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(audio.written()) != 1 || string(audio.written()[0].Payload) != string([]byte{3, 4}) {
		t.Fatalf("audio writes = %#v", audio.written())
	}
	gemini.mu.Lock()
	sent := append([][]byte(nil), gemini.sent...)
	gemini.mu.Unlock()
	if len(sent) != 1 || string(sent[0]) != string([]byte{1, 2}) {
		t.Fatalf("Gemini audio = %#v", sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Kind != geminilive.EventTurnComplete {
		t.Fatalf("events = %#v", got)
	}
}

func TestBridgeCancellationClosesBothSides(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	audio := newFakeAudio(nil, nil)
	gemini := &fakeGemini{closed: make(chan struct{})}
	b := New(audio, audio, gemini, nil)
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after cancellation")
	}
	select {
	case <-audio.closed:
	case <-time.After(time.Second):
		t.Fatal("audio side was not closed")
	}
	select {
	case <-gemini.closed:
	case <-time.After(time.Second):
		t.Fatal("Gemini side was not closed")
	}
}

func TestBridgePropagatesEventsWithoutExecutingTools(t *testing.T) {
	want := []geminilive.Event{{Kind: geminilive.EventToolCall, ToolCalls: []geminilive.ToolCall{{Name: "schedule"}}}, {Kind: geminilive.EventInterrupted}, {Kind: geminilive.EventTurnComplete}}
	audio := newFakeAudio(nil, io.EOF)
	gemini := &fakeGemini{events: want, closed: make(chan struct{})}
	var got []geminilive.Event
	b := New(audio, audio, gemini, func(_ context.Context, event geminilive.Event) error { got = append(got, event); return nil })
	if err := b.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].Name != "schedule" {
		t.Fatalf("tool event = %#v", got[0])
	}
}

func TestBridgeRejectsIncompatibleFormats(t *testing.T) {
	audio := newFakeAudio([]audiosocket.Frame{{Type: audiosocket.TypeSlin, Payload: []byte{1}}}, io.EOF)
	gemini := &fakeGemini{closed: make(chan struct{})}
	err := New(audio, audio, gemini, nil).Run(context.Background())
	if !errors.Is(err, ErrFormatIncompatible) {
		t.Fatalf("error = %v, want ErrFormatIncompatible", err)
	}
}

func TestBridgeRejectsIncompatibleGeminiAudio(t *testing.T) {
	audio := newFakeAudio(nil, io.EOF)
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventAudio, Audio: []byte{1}, AudioMimeType: "audio/pcm;rate=16000"}}, closed: make(chan struct{})}
	err := New(audio, audio, gemini, nil).Run(context.Background())
	if !errors.Is(err, ErrFormatIncompatible) {
		t.Fatalf("error = %v, want ErrFormatIncompatible", err)
	}
}

type blockingAudio struct {
	closed chan struct{}
	once   sync.Once
}

func (b *blockingAudio) ReadFrame() (audiosocket.Frame, error) {
	<-b.closed
	return audiosocket.Frame{}, io.EOF
}
func (b *blockingAudio) WriteFrame(audiosocket.Frame) error { return nil }
func (b *blockingAudio) Close() error                       { b.once.Do(func() { close(b.closed) }); return nil }

func TestBridgeGeminiDisconnectClosesAudio(t *testing.T) {
	audio := &blockingAudio{closed: make(chan struct{})}
	disconnect := errors.New("gemini disconnected")
	gemini := &fakeGemini{recvErr: disconnect, closed: make(chan struct{})}
	err := New(audio, audio, gemini, nil).Run(context.Background())
	if !errors.Is(err, disconnect) {
		t.Fatalf("error = %v, want %v", err, disconnect)
	}
	select {
	case <-audio.closed:
	default:
		t.Fatal("audio side was not closed after Gemini disconnect")
	}
}

func TestBridgeHandlerErrorCancelsSession(t *testing.T) {
	audio := newFakeAudio(nil, io.EOF)
	handlerErr := errors.New("event sink stopped")
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventInterrupted}}, closed: make(chan struct{})}
	err := New(audio, audio, gemini, func(context.Context, geminilive.Event) error { return handlerErr }).Run(context.Background())
	if !errors.Is(err, handlerErr) {
		t.Fatalf("error = %v, want %v", err, handlerErr)
	}
}
