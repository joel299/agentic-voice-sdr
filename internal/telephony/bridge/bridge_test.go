package bridge

import (
	"context"
	"errors"
	"io"
	"net"
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

type fakeLifecycle struct {
	mu          sync.Mutex
	authorized  bool
	failures    []error
	completions int
}

func (f *fakeLifecycle) ModelAudioAuthorized() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorized
}
func (f *fakeLifecycle) CompleteActive(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions++
	return nil
}
func (f *fakeLifecycle) FailActive(_ context.Context, err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, err)
	return nil
}

func TestBridgeUnauthorizedModelAudioIsDropped(t *testing.T) {
	output := newFakeAudio(nil, io.EOF)
	lifecycle := &fakeLifecycle{}
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventAudio, Audio: []byte{1}, AudioMimeType: "audio/pcm;rate=24000"}, {Kind: geminilive.EventClosed}}, closed: make(chan struct{})}
	if err := New(nil, output, gemini, nil, lifecycle).runEgress(context.Background(), func() {}); err != nil {
		t.Fatalf("runEgress() error = %v", err)
	}
	if writes := output.written(); len(writes) != 0 {
		t.Fatalf("unauthorized audio writes = %#v", writes)
	}
}

func TestBridgeAuthorizedModelAudioWritesSLIN24(t *testing.T) {
	output := newFakeAudio(nil, io.EOF)
	lifecycle := &fakeLifecycle{authorized: true}
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventAudio, Audio: []byte{1, 2}, AudioMimeType: "audio/pcm;rate=24000"}, {Kind: geminilive.EventClosed}}, closed: make(chan struct{})}
	if err := New(nil, output, gemini, nil, lifecycle).runEgress(context.Background(), func() {}); err != nil {
		t.Fatalf("runEgress() error = %v", err)
	}
	writes := output.written()
	if len(writes) != 1 || writes[0].Type != audiosocket.TypeSlin24 || string(writes[0].Payload) != string([]byte{1, 2}) {
		t.Fatalf("authorized audio writes = %#v", writes)
	}
}

func TestBridgeUnauthorizedThenAuthorizedOnlyWritesSecondAudio(t *testing.T) {
	output := newFakeAudio(nil, io.EOF)
	lifecycle := &sequencedLifecycle{}
	gemini := &fakeGemini{events: []geminilive.Event{
		{Kind: geminilive.EventAudio, Audio: []byte{1}, AudioMimeType: "audio/pcm;rate=24000"},
		{Kind: geminilive.EventAudio, Audio: []byte{2}, AudioMimeType: "audio/pcm;rate=24000"},
		{Kind: geminilive.EventClosed},
	}, closed: make(chan struct{})}
	if err := New(nil, output, gemini, nil, lifecycle).runEgress(context.Background(), func() {}); err != nil {
		t.Fatalf("runEgress() error = %v", err)
	}
	writes := output.written()
	if len(writes) != 1 || string(writes[0].Payload) != string([]byte{2}) {
		t.Fatalf("writes = %#v, want only authorized second audio", writes)
	}
}

type sequencedLifecycle struct {
	mu    sync.Mutex
	calls int
}

func (s *sequencedLifecycle) ModelAudioAuthorized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.calls > 1
}
func (s *sequencedLifecycle) CompleteActive(context.Context) error    { return nil }
func (s *sequencedLifecycle) FailActive(context.Context, error) error { return nil }

func TestBridgeTurnCompleteCompletesActiveResponse(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventTurnComplete}, {Kind: geminilive.EventClosed}}, closed: make(chan struct{})}
	if err := New(nil, newFakeAudio(nil, io.EOF), gemini, nil, lifecycle).runEgress(context.Background(), func() {}); err != nil {
		t.Fatalf("runEgress() error = %v", err)
	}
	if lifecycle.completions != 1 {
		t.Fatalf("CompleteActive calls = %d, want 1", lifecycle.completions)
	}
}

func TestBridgeFailureEventsFailActiveResponse(t *testing.T) {
	for _, event := range []geminilive.Event{{Kind: geminilive.EventInterrupted}, {Kind: geminilive.EventAPIError}} {
		t.Run(string(event.Kind), func(t *testing.T) {
			lifecycle := &fakeLifecycle{}
			gemini := &fakeGemini{events: []geminilive.Event{event, {Kind: geminilive.EventClosed}}, closed: make(chan struct{})}
			if err := New(nil, newFakeAudio(nil, io.EOF), gemini, func(context.Context, geminilive.Event) error { return errors.New("stop after failure event") }, lifecycle).runEgress(context.Background(), func() {}); err == nil {
				t.Fatal("runEgress() unexpectedly completed")
			}
			if len(lifecycle.failures) != 1 || lifecycle.failures[0] == nil {
				t.Fatalf("FailActive calls = %#v, want one sanitized error", lifecycle.failures)
			}
		})
	}
}

func TestBridgeClosedFailsActiveResponse(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	gemini := &fakeGemini{events: []geminilive.Event{{Kind: geminilive.EventClosed}}, closed: make(chan struct{})}
	if err := New(nil, newFakeAudio(nil, io.EOF), gemini, nil, lifecycle).runEgress(context.Background(), func() {}); err != nil {
		t.Fatalf("runEgress() error = %v", err)
	}
	if len(lifecycle.failures) != 1 || !errors.Is(lifecycle.failures[0], ErrSessionClosed) {
		t.Fatalf("FailActive calls = %#v, want ErrSessionClosed", lifecycle.failures)
	}
}

func TestBridgeReceiveFailureFailsActiveBeforeReturning(t *testing.T) {
	disconnect := errors.New("provider payload must not escape")
	lifecycle := &fakeLifecycle{}
	gemini := &fakeGemini{recvErr: disconnect, closed: make(chan struct{})}
	err := New(nil, newFakeAudio(nil, io.EOF), gemini, nil, lifecycle).runEgress(context.Background(), func() {})
	if !errors.Is(err, disconnect) {
		t.Fatalf("runEgress() error = %v, want original receive error", err)
	}
	if len(lifecycle.failures) != 1 || lifecycle.failures[0] == disconnect {
		t.Fatalf("FailActive calls = %#v, want sanitized failure before return", lifecycle.failures)
	}
}

func TestBridgeRoutesAudioBothDirections(t *testing.T) {
	audio := newOrderedAudio([]audiosocket.Frame{{Type: audiosocket.TypeSlin16, Payload: []byte{1, 2}}})
	gemini := newOrderedGemini()
	var got []geminilive.Event
	var mu sync.Mutex
	lifecycle := &fakeLifecycle{authorized: true}
	b := New(audio, audio, gemini, func(_ context.Context, event geminilive.Event) error {
		mu.Lock()
		got = append(got, event)
		mu.Unlock()
		return nil
	}, lifecycle)
	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case sent := <-gemini.sentCh:
		if string(sent) != string([]byte{1, 2}) {
			t.Fatalf("Gemini audio = %#v", sent)
		}
	case <-time.After(time.Second):
		t.Fatal("audio was not sent to Gemini")
	}
	audio.Close()
	gemini.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{3, 4}, AudioMimeType: "audio/pcm;rate=24000"}
	gemini.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not finish")
	}
	if writes := audio.written(); len(writes) != 1 || string(writes[0].Payload) != string([]byte{3, 4}) {
		t.Fatalf("audio writes = %#v", writes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Kind != geminilive.EventTurnComplete {
		t.Fatalf("events = %#v", got)
	}
}

type openAudio struct {
	mu       sync.Mutex
	frames   []audiosocket.Frame
	closed   chan struct{}
	closeOne sync.Once
}

func (a *openAudio) ReadFrame() (audiosocket.Frame, error) {
	a.mu.Lock()
	if len(a.frames) > 0 {
		frame := a.frames[0]
		a.frames = a.frames[1:]
		a.mu.Unlock()
		return frame, nil
	}
	a.mu.Unlock()
	<-a.closed
	return audiosocket.Frame{}, io.EOF
}
func (a *openAudio) WriteFrame(audiosocket.Frame) error { return nil }
func (a *openAudio) Close() error                       { a.closeOne.Do(func() { close(a.closed) }); return nil }

func TestBridgeKeepsReceivingAfterTurnCompleteForMultiTurnCall(t *testing.T) {
	audio := &openAudio{
		frames: []audiosocket.Frame{
			{Type: audiosocket.TypeSlin16, Payload: []byte{1}},
			{Type: audiosocket.TypeSlin16, Payload: []byte{2}},
		},
		closed: make(chan struct{}),
	}
	gemini := &fakeGemini{events: []geminilive.Event{
		{Kind: geminilive.EventAudio, Audio: []byte{3}, AudioMimeType: "audio/pcm;rate=24000"},
		{Kind: geminilive.EventTurnComplete},
		{Kind: geminilive.EventAudio, Audio: []byte{4}, AudioMimeType: "audio/pcm;rate=24000"},
		{Kind: geminilive.EventTurnComplete},
	}, closed: make(chan struct{})}
	turns := make(chan struct{}, 2)
	lifecycle := &fakeLifecycle{authorized: true}
	b := New(audio, audio, gemini, func(_ context.Context, event geminilive.Event) error {
		if event.Kind == geminilive.EventTurnComplete {
			turns <- struct{}{}
		}
		return nil
	}, lifecycle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	select {
	case <-turns:
	case <-time.After(time.Second):
		t.Fatal("first turnComplete was not delivered")
	}
	select {
	case err := <-done:
		t.Fatalf("bridge ended after first turnComplete: %v", err)
	default:
	}
	select {
	case <-turns:
	case <-time.After(time.Second):
		t.Fatal("second turnComplete was not delivered")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("multi-turn bridge did not stop after cancellation")
	}
	gemini.mu.Lock()
	sent := append([][]byte(nil), gemini.sent...)
	gemini.mu.Unlock()
	if len(sent) != 2 || string(sent[0]) != string([]byte{1}) || string(sent[1]) != string([]byte{2}) {
		t.Fatalf("Gemini audio = %#v", sent)
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

func TestBridgeClosesProductionAudioSocketAndUnblocksRead(t *testing.T) {
	peer, conn := net.Pipe()
	defer peer.Close()
	stream := audiosocket.NewStream(conn, conn)
	disconnect := errors.New("gemini disconnected")
	gemini := &fakeGemini{recvErr: disconnect, closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- New(stream, stream, gemini, nil).Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, disconnect) {
			t.Fatalf("Run() error = %v, want %v", err, disconnect)
		}
	case <-time.After(time.Second):
		t.Fatal("Bridge.Run remained blocked on production AudioSocket ReadFrame")
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

type orderedAudio struct {
	mu           sync.Mutex
	frames       []audiosocket.Frame
	writes       []audiosocket.Frame
	eof          chan struct{}
	eofOnce      sync.Once
	next         chan audiosocket.Frame
	eofAfterNext bool
	nextConsumed bool
	closed       chan struct{}
	closeOne     sync.Once
}

func newOrderedAudio(frames []audiosocket.Frame) *orderedAudio {
	return &orderedAudio{frames: frames, eof: make(chan struct{}), next: make(chan audiosocket.Frame), closed: make(chan struct{})}
}

func (a *orderedAudio) ReadFrame() (audiosocket.Frame, error) {
	a.mu.Lock()
	if a.nextConsumed {
		a.mu.Unlock()
		return audiosocket.Frame{}, io.EOF
	}
	if len(a.frames) > 0 {
		frame := a.frames[0]
		a.frames = a.frames[1:]
		a.mu.Unlock()
		return frame, nil
	}
	a.mu.Unlock()
	select {
	case frame := <-a.next:
		a.mu.Lock()
		eofAfterNext := a.eofAfterNext
		a.nextConsumed = true
		a.mu.Unlock()
		if eofAfterNext {
			a.eofOnce.Do(func() { close(a.eof) })
		}
		return frame, nil
	case <-a.closed:
		return audiosocket.Frame{}, io.EOF
	}
}

func (a *orderedAudio) appendFrame(frame audiosocket.Frame) {
	a.mu.Lock()
	a.eofAfterNext = true
	a.mu.Unlock()
	a.next <- frame
}

func (a *orderedAudio) WriteFrame(frame audiosocket.Frame) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes = append(a.writes, frame)
	return nil
}

func (a *orderedAudio) Close() error {
	a.closeOne.Do(func() { close(a.closed) })
	return nil
}

func (a *orderedAudio) written() []audiosocket.Frame {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audiosocket.Frame(nil), a.writes...)
}

type orderedGemini struct {
	mu       sync.Mutex
	sent     [][]byte
	sentCh   chan []byte
	events   chan geminilive.Event
	closed   chan struct{}
	closeOne sync.Once
}

func newOrderedGemini() *orderedGemini {
	return &orderedGemini{sentCh: make(chan []byte, 4), events: make(chan geminilive.Event, 4), closed: make(chan struct{})}
}

func (g *orderedGemini) SendAudio(_ context.Context, audio []byte) error {
	copyAudio := append([]byte(nil), audio...)
	g.mu.Lock()
	g.sent = append(g.sent, copyAudio)
	g.mu.Unlock()
	g.sentCh <- copyAudio
	return nil
}

func (g *orderedGemini) EndAudio(context.Context) error { return nil }

func (g *orderedGemini) Receive(ctx context.Context) (geminilive.Event, error) {
	select {
	case event := <-g.events:
		return event, nil
	case <-ctx.Done():
		return geminilive.Event{}, ctx.Err()
	}
}

func (g *orderedGemini) Close() error {
	g.closeOne.Do(func() { close(g.closed) })
	return nil
}

func TestBridgeStaleTurnCompleteCannotFinishNewerInput(t *testing.T) {
	audio := newOrderedAudio([]audiosocket.Frame{
		{Type: audiosocket.TypeSlin16, Payload: []byte{1}},
	})
	gemini := newOrderedGemini()
	lifecycle := &fakeLifecycle{authorized: true}
	turnOneComplete := make(chan struct{}, 1)
	b := New(audio, audio, gemini, func(_ context.Context, event geminilive.Event) error {
		if event.Kind == geminilive.EventTurnComplete {
			select {
			case turnOneComplete <- struct{}{}:
			default:
			}
		}
		return nil
	}, lifecycle)
	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()

	select {
	case got := <-gemini.sentCh:
		if string(got) != string([]byte{1}) {
			t.Fatalf("first Gemini audio = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first audio was not sent")
	}
	gemini.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{11}, AudioMimeType: "audio/pcm;rate=24000"}
	gemini.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	select {
	case <-turnOneComplete:
	case <-time.After(time.Second):
		t.Fatal("first TurnComplete was not processed")
	}
	audio.appendFrame(audiosocket.Frame{Type: audiosocket.TypeSlin16, Payload: []byte{2}})

	select {
	case got := <-gemini.sentCh:
		if string(got) != string([]byte{2}) {
			t.Fatalf("second Gemini audio = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second audio was not sent")
	}
	select {
	case err := <-done:
		t.Fatalf("bridge ended on stale TurnComplete: %v", err)
	case <-audio.eof:
	}

	gemini.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{22}, AudioMimeType: "audio/pcm;rate=24000"}
	deadline := time.After(time.Second)
	for {
		if writes := audio.written(); len(writes) == 2 {
			if string(writes[1].Payload) != string([]byte{22}) {
				t.Fatalf("second response payload = %v", writes[1].Payload)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("second response audio was not delivered; writes = %#v", audio.written())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	gemini.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not finish after TurnComplete for second input")
	}
}

func TestBridgeCompletedTurnAllowsInputCloseWithoutExtraTurn(t *testing.T) {
	audio := newOrderedAudio([]audiosocket.Frame{{Type: audiosocket.TypeSlin16, Payload: []byte{1}}})
	gemini := newOrderedGemini()
	lifecycle := &fakeLifecycle{authorized: true}
	done := make(chan error, 1)
	go func() { done <- New(audio, audio, gemini, nil, lifecycle).Run(context.Background()) }()
	select {
	case got := <-gemini.sentCh:
		if string(got) != string([]byte{1}) {
			t.Fatalf("Gemini audio = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("audio was not sent")
	}
	audio.Close()
	gemini.events <- geminilive.Event{Kind: geminilive.EventAudio, Audio: []byte{3}, AudioMimeType: "audio/pcm;rate=24000"}
	gemini.events <- geminilive.Event{Kind: geminilive.EventTurnComplete}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not finish after the latest TurnComplete")
	}
	if writes := audio.written(); len(writes) != 1 || string(writes[0].Payload) != string([]byte{3}) {
		t.Fatalf("writes = %#v", writes)
	}
}
