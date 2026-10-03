package bridge

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

type receivedResponse struct {
	event geminilive.Event
	err   error
	at    time.Time
}

// One bounded receive owner can cancel an in-flight blocked PCM write as soon
// as an interruption arrives. It never interprets an interim lead as a JEV turn.
type responseReceiver struct {
	events      chan receivedResponse
	done        chan struct{}
	interrupted atomic.Bool
	interruptAt atomic.Int64
	mu          sync.Mutex
	cancelAudio context.CancelFunc
}

func newResponseReceiver(ctx context.Context, s geminilive.ControlledResponseSession) *responseReceiver {
	r := &responseReceiver{events: make(chan receivedResponse, 4), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for {
			event, err := s.Receive(ctx)
			if event.Kind == geminilive.EventInterrupted {
				r.mu.Lock()
				r.interrupted.Store(true)
				r.interruptAt.Store(time.Now().UnixNano())
				if r.cancelAudio != nil {
					r.cancelAudio()
				}
				r.mu.Unlock()
			}
			select {
			case r.events <- receivedResponse{event: event, err: err, at: time.Now()}:
			case <-ctx.Done():
				return
			}
			if err != nil || event.Kind == geminilive.EventClosed {
				return
			}
		}
	}()
	return r
}
func (r *responseReceiver) audioContext(ctx context.Context) (context.Context, context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, cancel := context.WithCancel(ctx)
	r.cancelAudio = cancel
	if r.interrupted.Load() {
		cancel()
	}
	return c, func() { cancel(); r.mu.Lock(); r.cancelAudio = nil; r.mu.Unlock() }
}
func (r *responseReceiver) receive(ctx context.Context) (geminilive.Event, time.Time, error) {
	select {
	case e := <-r.events:
		return e.event, e.at, e.err
	case <-ctx.Done():
		return geminilive.Event{}, time.Time{}, ctx.Err()
	}
}
