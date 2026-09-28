package voiceflow

import (
	"context"
	"sync"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/bridge"
)

// responseCoordinator is deliberately narrower than turnloop.Coordinator so
// the lifecycle adapter can be tested without coupling it to Begin.
type responseCoordinator interface {
	Complete(conversation.ResponseKey) error
	Fail(conversation.ResponseKey) error
}

// ResponseLifecycleAdapter adapts the coordinator lifecycle to the bridge. It
// owns at most one active response key and never stores provider failure text.
type ResponseLifecycleAdapter struct {
	mu          sync.Mutex
	cond        *sync.Cond
	coordinator responseCoordinator
	active      conversation.ResponseKey
	generation  uint64
	inFlight    bool
}

var _ bridge.ResponseLifecycle = (*ResponseLifecycleAdapter)(nil)

func NewResponseLifecycleAdapter(coordinator responseCoordinator) *ResponseLifecycleAdapter {
	a := &ResponseLifecycleAdapter{coordinator: coordinator}
	a.cond = sync.NewCond(&a.mu)
	return a
}

// Bind makes key the current response. A zero key clears the active response.
func (a *ResponseLifecycleAdapter) Bind(key conversation.ResponseKey) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.ensureCondLocked()
	for a.inFlight {
		a.cond.Wait()
	}
	a.generation++
	a.active = key
	a.mu.Unlock()
}

func (a *ResponseLifecycleAdapter) CaptureActive() bridge.ResponseTurnLease {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	key := a.active
	generation := a.generation
	a.mu.Unlock()
	if key == (conversation.ResponseKey{}) {
		return nil
	}
	return &responseTurnLease{owner: a, key: key, generation: generation}
}

func (a *ResponseLifecycleAdapter) FailActive(ctx context.Context, _ error) error {
	if a == nil {
		return nil
	}
	key, generation := a.current()
	if key == (conversation.ResponseKey{}) || !a.beginOperation(key, generation) {
		return nil
	}
	err := a.coordinator.Fail(key)
	a.finishOperation(key, generation)
	return err
}

func (a *ResponseLifecycleAdapter) current() (conversation.ResponseKey, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active, a.generation
}

func (a *ResponseLifecycleAdapter) ensureCondLocked() {
	if a.cond == nil {
		a.cond = sync.NewCond(&a.mu)
	}
}

func (a *ResponseLifecycleAdapter) beginOperation(key conversation.ResponseKey, generation uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active != key || a.generation != generation || a.inFlight || a.coordinator == nil {
		return false
	}
	a.inFlight = true
	return true
}

func (a *ResponseLifecycleAdapter) finishOperation(key conversation.ResponseKey, generation uint64) {
	a.mu.Lock()
	a.ensureCondLocked()
	if a.active == key && a.generation == generation {
		a.active = conversation.ResponseKey{}
	}
	a.inFlight = false
	a.cond.Broadcast()
	a.mu.Unlock()
}

type responseTurnLease struct {
	owner      *ResponseLifecycleAdapter
	key        conversation.ResponseKey
	generation uint64
}

func (l *responseTurnLease) ModelAudioAuthorized() bool {
	if l == nil || l.owner == nil {
		return false
	}
	key, generation := l.owner.current()
	return key == l.key && generation == l.generation
}

func (l *responseTurnLease) Complete(_ context.Context) error {
	if l == nil || l.owner == nil || !l.owner.beginOperation(l.key, l.generation) {
		return nil
	}
	err := l.owner.coordinator.Complete(l.key)
	l.owner.finishOperation(l.key, l.generation)
	return err
}

func (l *responseTurnLease) Fail(_ context.Context, _ error) error {
	if l == nil || l.owner == nil || !l.owner.beginOperation(l.key, l.generation) {
		return nil
	}
	err := l.owner.coordinator.Fail(l.key)
	l.owner.finishOperation(l.key, l.generation)
	return err
}
