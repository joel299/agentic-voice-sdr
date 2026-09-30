// Package callservice coordinates owner-authorized outbound call lifecycle
// operations over a provider-neutral telephony control boundary.
package callservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

var (
	ErrInvalidDestination = errors.New("invalid destination")
	ErrDestinationDenied  = errors.New("destination is not allowed")
	ErrNotRegistered      = errors.New("telephony provider is not registered")
	ErrCallActive         = errors.New("an active call already exists")
	ErrCallNotFound       = errors.New("call not found")
	ErrCallNotActive      = errors.New("call is not active")
	ErrHangupRequested    = errors.New("hangup already requested")
	ErrProviderFailure    = errors.New("telephony provider request failed")
)

type Status string

const (
	StatusDialing   Status = "dialing"
	StatusRinging   Status = "ringing"
	StatusConnected Status = "connected"
	StatusCompleted Status = "completed"
	StatusBusy      Status = "busy"
	StatusNoAnswer  Status = "no_answer"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

func (s Status) terminal() bool {
	switch s {
	case StatusCompleted, StatusBusy, StatusNoAnswer, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

type Call struct {
	CallID         string `json:"call_id"`
	ProviderCallID string `json:"provider_call_id,omitempty"`
	To             string `json:"to"`
	Status         Status `json:"status"`
}

type DestinationPolicy interface {
	Normalize(string) (string, error)
	Allows(string) bool
}

type Allowlist struct{ destinations map[string]struct{} }

func NewAllowlist(values []string) (*Allowlist, error) {
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := NormalizeDestination(value)
		if err != nil {
			return nil, err
		}
		allowed[normalized] = struct{}{}
	}
	return &Allowlist{destinations: allowed}, nil
}

func (p *Allowlist) Normalize(value string) (string, error) { return NormalizeDestination(value) }
func (p *Allowlist) Allows(value string) bool {
	_, ok := p.destinations[value]
	return ok
}

func NormalizeDestination(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrInvalidDestination
	}
	var digits strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' && digits.Len() == 0:
			// A leading plus is the only accepted dialing prefix.
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", ErrInvalidDestination
		}
	}
	number := digits.String()
	if len(number) < 8 || len(number) > 15 {
		return "", ErrInvalidDestination
	}
	return "+" + number, nil
}

type Service struct {
	provider control.Provider
	policy   DestinationPolicy
	ctx      context.Context
	cancel   context.CancelFunc

	mu              sync.RWMutex
	calls           map[string]Call
	activeID        string
	starting        bool
	requested       map[string]bool
	uncertain       map[string]bool
	hangupUncertain map[string]bool
}

func New(provider control.Provider, policy DestinationPolicy) (*Service, error) {
	if provider == nil || policy == nil {
		return nil, errors.New("call service dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{provider: provider, policy: policy, ctx: ctx, cancel: cancel, calls: make(map[string]Call), requested: make(map[string]bool), uncertain: make(map[string]bool), hangupUncertain: make(map[string]bool)}
	go s.consumeEvents()
	return s, nil
}

func (s *Service) Start(ctx context.Context, destination string) (Call, error) {
	canonical, err := s.policy.Normalize(destination)
	if err != nil {
		return Call{}, ErrInvalidDestination
	}
	if !s.policy.Allows(canonical) {
		return Call{}, ErrDestinationDenied
	}
	s.mu.Lock()
	if s.starting {
		s.mu.Unlock()
		return Call{}, ErrCallActive
	}
	activeID := s.activeID
	needsReconcile := activeID != "" && (s.uncertain[activeID] || s.hangupUncertain[activeID])
	if activeID != "" && !needsReconcile {
		s.mu.Unlock()
		return Call{}, ErrCallActive
	}
	s.starting = true
	s.mu.Unlock()
	if needsReconcile {
		var reconcileErr error
		s.mu.RLock()
		dialUncertain := s.uncertain[activeID]
		s.mu.RUnlock()
		if dialUncertain {
			reconcileErr = s.Reconcile(ctx, activeID)
		} else {
			reconcileErr = s.reconcileHangup(ctx, activeID)
		}
		if reconcileErr != nil {
			s.mu.Lock()
			s.starting = false
			s.mu.Unlock()
			return Call{}, ErrCallActive
		}
		s.mu.Lock()
		stillActive := s.activeID != ""
		if stillActive {
			s.starting = false
		}
		s.mu.Unlock()
		if stillActive {
			return Call{}, ErrCallActive
		}
	}

	registration, err := s.provider.RegistrationStatus(ctx)
	if err != nil {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return Call{}, ErrProviderFailure
	}
	if registration.State != control.RegistrationRegistered {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return Call{}, ErrNotRegistered
	}
	providerCalls, err := s.provider.ActiveCalls(ctx)
	if err != nil {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return Call{}, ErrProviderFailure
	}
	if len(providerCalls) != 0 {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return Call{}, ErrCallActive
	}
	callID, err := newID()
	if err != nil {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return Call{}, ErrProviderFailure
	}
	call := Call{CallID: callID, To: canonical, Status: StatusDialing}
	s.mu.Lock()
	s.starting = false
	s.calls[callID] = call
	s.activeID = callID
	s.mu.Unlock()

	if _, err := s.provider.Dial(ctx, canonical); err != nil {
		s.mu.Lock()
		call := s.calls[callID]
		certainty := control.DispatchCertaintyOf(err)
		if certainty == control.DispatchMaybeDispatched {
			s.uncertain[callID] = true
		} else {
			call.Status = StatusFailed
			s.calls[callID] = call
			if s.activeID == callID {
				s.activeID = ""
			}
		}
		s.mu.Unlock()
		if certainty == control.DispatchMaybeDispatched {
			_ = s.Reconcile(ctx, callID)
		}
		return Call{}, ErrProviderFailure
	}
	return s.Get(callID)
}

func (s *Service) Get(callID string) (Call, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	call, ok := s.calls[callID]
	if !ok {
		return Call{}, ErrCallNotFound
	}
	return call, nil
}

func (s *Service) Hangup(ctx context.Context, callID string) (Call, error) {
	s.mu.Lock()
	call, ok := s.calls[callID]
	if !ok {
		s.mu.Unlock()
		return Call{}, ErrCallNotFound
	}
	if s.activeID != callID || call.Status.terminal() {
		s.mu.Unlock()
		return Call{}, ErrCallNotActive
	}
	if s.requested[callID] && !s.hangupUncertain[callID] {
		s.mu.Unlock()
		return Call{}, ErrHangupRequested
	}
	if s.requested[callID] && s.hangupUncertain[callID] {
		s.mu.Unlock()
		if err := s.reconcileHangup(ctx, callID); err != nil {
			return Call{}, ErrProviderFailure
		}
		s.mu.RLock()
		stillActive := s.activeID == callID
		call = s.calls[callID]
		s.mu.RUnlock()
		if stillActive {
			return call, ErrHangupRequested
		}
		return call, nil
	}
	// Mark before I/O so concurrent retries cannot issue a second command.
	s.requested[callID] = true
	s.mu.Unlock()
	if _, err := s.provider.Hangup(ctx); err != nil {
		certainty := control.DispatchCertaintyOf(err)
		s.mu.Lock()
		if certainty == control.DispatchNotDispatched || certainty == control.DispatchRejected {
			delete(s.requested, callID)
		} else {
			s.hangupUncertain[callID] = true
		}
		s.mu.Unlock()
		if certainty == control.DispatchMaybeDispatched {
			_ = s.reconcileHangup(ctx, callID)
		}
		return Call{}, ErrProviderFailure
	}
	return s.Get(callID)
}

// Reconcile compares an uncertain dial outcome with the provider's current
// inventory. An unreadable or nonmatching nonempty inventory keeps the gate.
func (s *Service) Reconcile(ctx context.Context, callID string) error {
	inventory, err := s.provider.ActiveCalls(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok || s.activeID != callID || !s.uncertain[callID] {
		return nil
	}
	if len(inventory) == 0 {
		call.Status = StatusFailed
		s.calls[callID] = call
		s.activeID = ""
		delete(s.uncertain, callID)
		return nil
	}
	for _, active := range inventory {
		if !inventoryMatches(active, call) {
			continue
		}
		call.ProviderCallID = active.ProviderCallID
		switch active.State {
		case control.CallStateConnected:
			call.Status = StatusConnected
		case control.CallStateRinging:
			call.Status = StatusRinging
		case control.CallStateOutgoing:
			call.Status = StatusDialing
		}
		s.calls[callID] = call
		delete(s.uncertain, callID)
		return nil
	}
	return nil
}

func (s *Service) reconcileHangup(ctx context.Context, callID string) error {
	inventory, err := s.provider.ActiveCalls(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok || s.activeID != callID || !s.hangupUncertain[callID] {
		return nil
	}
	if len(inventory) != 0 {
		return nil
	}
	if call.Status == StatusConnected {
		call.Status = StatusCompleted
	} else {
		call.Status = StatusCanceled
	}
	s.calls[callID] = call
	s.activeID = ""
	delete(s.hangupUncertain, callID)
	return nil
}

func inventoryMatches(active control.ActiveCall, call Call) bool {
	if active.ProviderCallID != "" && call.ProviderCallID != "" {
		return active.ProviderCallID == call.ProviderCallID
	}
	if active.PeerURI == "" {
		return false
	}
	peer := strings.TrimPrefix(strings.TrimSpace(active.PeerURI), "sip:")
	peer = strings.TrimPrefix(peer, "sips:")
	user := strings.SplitN(peer, "@", 2)[0]
	user = strings.TrimPrefix(user, "+")
	return user == strings.TrimPrefix(call.To, "+")
}

func (s *Service) Close() { s.cancel() }

func (s *Service) consumeEvents() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case event, ok := <-s.provider.Events():
			if !ok {
				return
			}
			s.applyEvent(event)
		}
	}
}

func (s *Service) applyEvent(event control.Event) {
	if !strings.EqualFold(event.Class, "call") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID == "" {
		return
	}
	call := s.calls[s.activeID]
	if call.ProviderCallID == "" {
		if !eventMatchesDestination(event, call.To) {
			return
		}
		call.ProviderCallID = event.CallID
		delete(s.uncertain, call.CallID)
	} else if event.CallID != call.ProviderCallID {
		return
	}
	switch strings.ToUpper(event.Type) {
	case "CALL_OUTGOING", "CALL_SETUP":
		call.Status = StatusDialing
	case "CALL_PROGRESS", "CALL_SESSION_PROGRESS", "CALL_RINGING", "CALL_ALERTING":
		call.Status = StatusRinging
	case "CALL_ESTABLISHED", "CALL_ANSWERED":
		call.Status = StatusConnected
	case "CALL_CLOSED", "CALL_TERMINATED", "CALL_FAILED":
		call.Status = statusFromEvent(event)
		if call.Status.terminal() {
			s.activeID = ""
			delete(s.uncertain, call.CallID)
			delete(s.hangupUncertain, call.CallID)
		}
	default:
		return
	}
	s.calls[call.CallID] = call
}

func eventMatchesDestination(event control.Event, destination string) bool {
	if event.CallID == "" {
		return false
	}
	if event.PeerURI == "" {
		return strings.EqualFold(event.Type, "CALL_OUTGOING") || strings.EqualFold(event.Type, "CALL_SETUP")
	}
	peer, err := url.Parse("sip:" + strings.TrimPrefix(event.PeerURI, "sip:"))
	if err != nil {
		return false
	}
	user := strings.Split(peer.Opaque, "@")[0]
	if peer.Opaque == "" {
		user = strings.Split(strings.TrimPrefix(event.PeerURI, "sip:"), "@")[0]
	}
	user = strings.TrimPrefix(user, "+")
	return user == strings.TrimPrefix(destination, "+")
}

func statusFromEvent(event control.Event) Status {
	if strings.EqualFold(event.Type, "CALL_FAILED") {
		return StatusFailed
	}
	switch event.State {
	case control.CallStateCompleted:
		return StatusCompleted
	case control.CallStateBusy:
		return StatusBusy
	case control.CallStateNoAnswer:
		return StatusNoAnswer
	case control.CallStateCanceled:
		return StatusCanceled
	default:
		return StatusFailed
	}
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate call id: %w", err)
	}
	return "call_" + hex.EncodeToString(raw[:]), nil
}

func IsActive(status Status) bool { return !status.terminal() }
