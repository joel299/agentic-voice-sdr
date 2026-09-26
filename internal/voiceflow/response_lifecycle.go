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
	coordinator responseCoordinator
	active      conversation.ResponseKey
}

var _ bridge.ResponseLifecycle = (*ResponseLifecycleAdapter)(nil)

func NewResponseLifecycleAdapter(coordinator responseCoordinator) *ResponseLifecycleAdapter {
	return &ResponseLifecycleAdapter{coordinator: coordinator}
}

// Bind makes key the current response. A zero key clears the active response.
func (a *ResponseLifecycleAdapter) Bind(key conversation.ResponseKey) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.active = key
	a.mu.Unlock()
}

func (a *ResponseLifecycleAdapter) CaptureActive() bridge.ResponseTurnLease {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	key := a.active
	a.mu.Unlock()
	if key == (conversation.ResponseKey{}) {
		return nil
	}
	return &responseTurnLease{owner: a, key: key}
}

func (a *ResponseLifecycleAdapter) FailActive(ctx context.Context, _ error) error {
	if a == nil {
		return nil
	}
	key := a.snapshot()
	if key == (conversation.ResponseKey{}) {
		return nil
	}
	if a.coordinator == nil {
		return nil
	}
	err := a.coordinator.Fail(key)
	a.clearIf(key)
	return err
}

func (a *ResponseLifecycleAdapter) snapshot() conversation.ResponseKey {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

func (a *ResponseLifecycleAdapter) clearIf(key conversation.ResponseKey) {
	a.mu.Lock()
	if a.active == key {
		a.active = conversation.ResponseKey{}
	}
	a.mu.Unlock()
}

type responseTurnLease struct {
	owner *ResponseLifecycleAdapter
	key   conversation.ResponseKey
}

func (l *responseTurnLease) ModelAudioAuthorized() bool {
	if l == nil || l.owner == nil {
		return false
	}
	return l.owner.snapshot() == l.key
}

func (l *responseTurnLease) Complete(_ context.Context) error {
	if l == nil || l.owner == nil || l.owner.coordinator == nil {
		return nil
	}
	err := l.owner.coordinator.Complete(l.key)
	l.owner.clearIf(l.key)
	return err
}

func (l *responseTurnLease) Fail(_ context.Context, _ error) error {
	if l == nil || l.owner == nil || l.owner.coordinator == nil {
		return nil
	}
	err := l.owner.coordinator.Fail(l.key)
	l.owner.clearIf(l.key)
	return err
}
