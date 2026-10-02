// Package callservice coordinates owner-authorized outbound call lifecycle
// operations over a provider-neutral telephony control boundary.
package callservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
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
	ErrPersistenceFailure = errors.New("call persistence failed")
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
	CallID          string     `json:"call_id"`
	ProviderCallID  string     `json:"provider_call_id,omitempty"`
	To              string     `json:"to"`
	Status          Status     `json:"status"`
	TerminalReason  string     `json:"terminal_reason,omitempty"`
	AIRuntimeStatus string     `json:"ai_runtime_status"`
	AIRuntimeStage  string     `json:"ai_runtime_stage"`
	AIFailureClass  string     `json:"ai_failure_class,omitempty"`
	AIFailureAt     *time.Time `json:"ai_failure_at,omitempty"`
	// Bounded per-call diagnostic timestamps retained for this process lifetime.
	// No arbitrary provider strings/audio are retained in these fields.
	AudioErrorSeen     bool       `json:"audio_error_seen"`
	AudioErrorAt       *time.Time `json:"audio_error_at,omitempty"`
	AudioErrorClass    string     `json:"audio_error_class,omitempty"`
	GeminiAudioFirstAt *time.Time `json:"gemini_audio_first_at,omitempty"`
	MediaEgressFirstAt *time.Time `json:"media_egress_first_at,omitempty"`
	CallFailedAt       *time.Time `json:"call_failed_at,omitempty"`
	CallClosedAt       *time.Time `json:"call_closed_at,omitempty"`
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
	callDone        map[string]chan struct{}
	activeID        string
	starting        bool
	requested       map[string]bool
	uncertain       map[string]bool
	hangupUncertain map[string]bool
	repository      voicecalldomain.CallRepository
	persistenceErr  error
}

func New(provider control.Provider, policy DestinationPolicy) (*Service, error) {
	return NewWithRepository(provider, policy, nil)
}

func NewWithRepository(provider control.Provider, policy DestinationPolicy, repository voicecalldomain.CallRepository) (*Service, error) {
	if provider == nil || policy == nil {
		return nil, errors.New("call service dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{provider: provider, policy: policy, ctx: ctx, cancel: cancel, calls: make(map[string]Call), callDone: make(map[string]chan struct{}), requested: make(map[string]bool), uncertain: make(map[string]bool), hangupUncertain: make(map[string]bool), repository: repository}
	go s.consumeEvents()
	return s, nil
}

func (s *Service) Start(ctx context.Context, destination string) (Call, error) {
	s.mu.RLock()
	persistenceErr := s.persistenceErr
	s.mu.RUnlock()
	if persistenceErr != nil {
		return Call{}, ErrPersistenceFailure
	}
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
			if errors.Is(reconcileErr, ErrPersistenceFailure) {
				return Call{}, ErrPersistenceFailure
			}
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
	call := Call{CallID: callID, To: canonical, Status: StatusDialing, AIRuntimeStatus: "not_started", AIRuntimeStage: "not_started"}
	if s.repository != nil {
		if err := s.repository.CreateCall(ctx, voicecalldomain.Call{ID: callID, Destination: canonical, Status: string(StatusDialing), Provider: "baresip"}); err != nil {
			s.mu.Lock()
			s.starting = false
			s.mu.Unlock()
			return Call{}, ErrPersistenceFailure
		}
	}
	s.mu.Lock()
	s.starting = false
	s.calls[callID] = call
	s.callDone[callID] = make(chan struct{})
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
			s.closeCallDoneLocked(callID)
			if s.activeID == callID {
				s.activeID = ""
			}
		}
		s.mu.Unlock()
		if certainty != control.DispatchMaybeDispatched && s.repository != nil {
			if persistErr := s.repository.UpdateLifecycle(ctx, callID, string(StatusFailed), "", "provider_dial_failed"); persistErr != nil {
				s.setPersistenceError(persistErr)
				return Call{}, ErrPersistenceFailure
			}
		}
		if certainty == control.DispatchMaybeDispatched {
			if reconcileErr := s.Reconcile(ctx, callID); errors.Is(reconcileErr, ErrPersistenceFailure) {
				return Call{}, ErrPersistenceFailure
			}
		}
		return Call{}, ErrProviderFailure
	}
	log.Printf("call_timeline api_call_id=%s provider_call_id=none event=dial_accepted at=%s dropped_ctrl_events=%d", callID, time.Now().UTC().Format(time.RFC3339Nano), droppedEvents(s.provider))
	return s.Get(callID)
}

func (s *Service) Get(callID string) (Call, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.persistenceErr != nil {
		return Call{}, ErrPersistenceFailure
	}
	call, ok := s.calls[callID]
	if !ok {
		return Call{}, ErrCallNotFound
	}
	return call, nil
}

// UpdateAIRuntimeStatus records AI state independently of the SIP call state.
// It never dispatches Hangup or otherwise changes telephony lifecycle.
func (s *Service) UpdateAIRuntimeStatus(ctx context.Context, callID, status, stage, failureClass string, failureAt *time.Time) error {
	if ctx == nil || callID == "" || !validAIRuntimeStatus(status) || !validAIRuntimeStage(stage) || !validAIFailureClass(failureClass) {
		return ErrProviderFailure
	}
	s.mu.Lock()
	call, ok := s.calls[callID]
	if !ok {
		s.mu.Unlock()
		return ErrCallNotFound
	}
	call.AIRuntimeStatus = status
	call.AIRuntimeStage = stage
	call.AIFailureClass = failureClass
	call.AIFailureAt = failureAt
	s.calls[callID] = call
	s.mu.Unlock()
	if s.repository != nil {
		if err := s.repository.UpdateAIRuntimeStatus(ctx, callID, status, stage, failureClass, failureAt); err != nil {
			s.setPersistenceError(err)
			return ErrPersistenceFailure
		}
	}
	return nil
}

func validAIRuntimeStage(value string) bool {
	switch value {
	case "not_started", "runtime_starting", "jev_config", "jev_client_init", "prompt_snapshot", "gemini_input_connect", "gemini_response_connect", "conversation_state", "turn_runtime_init", "bridge_init", "bridge_run", "media_ingress", "input_transcription_send", "input_transcription_receive", "input_transcription_handler", "jev_provider", "turn_directive", "gemini_response_send", "gemini_response_receive", "media_egress", "turn_complete", "degraded_mode", "runtime_shutdown", "runtime_unknown":
		return true
	default:
		return false
	}
}

func validAIRuntimeStatus(value string) bool {
	switch value {
	case "starting", "running", "failed", "degraded", "stopped":
		return true
	default:
		return false
	}
}

func validAIFailureClass(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "timeout", "canceled", "receive_failed", "provider_api", "media_closed", "runtime_error", "session_ended", "jev_config", "jev_client_init", "prompt_snapshot", "gemini_input_connect", "gemini_response_connect", "conversation_state", "turn_runtime_init", "bridge_init", "media_ingress", "input_transcription_send", "input_transcription_receive", "jev_provider", "turn_directive", "gemini_response_send", "gemini_response_receive", "media_egress", "provider_transport", "runtime_unknown":
		return true
	default:
		return false
	}
}

// ActiveCall returns the current canonical API call identity for media-session
// correlation. The provider's Baresip Call-ID remains a separate field.
func (s *Service) ActiveCall() (Call, bool) {
	if s == nil {
		return Call{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeID == "" {
		return Call{}, false
	}
	call, ok := s.calls[s.activeID]
	return call, ok
}

// WaitCallEnd waits until a provider terminal event or owner Hangup ends callID.
// A call already known to be terminal returns immediately. The notification is
// call-scoped so media supervisors can cancel AI processing without polling.
func (s *Service) WaitCallEnd(ctx context.Context, callID string) error {
	if ctx == nil || callID == "" {
		return ErrCallNotFound
	}
	s.mu.RLock()
	call, exists := s.calls[callID]
	done := s.callDone[callID]
	s.mu.RUnlock()
	if !exists {
		return ErrCallNotFound
	}
	if call.Status.terminal() {
		return nil
	}
	if done == nil {
		return ErrCallNotFound
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Hangup(ctx context.Context, callID string) (Call, error) {
	s.mu.Lock()
	if s.persistenceErr != nil {
		s.mu.Unlock()
		return Call{}, ErrPersistenceFailure
	}
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
			if errors.Is(err, ErrPersistenceFailure) {
				return Call{}, ErrPersistenceFailure
			}
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
	call, ok := s.calls[callID]
	if !ok || s.activeID != callID || !s.uncertain[callID] {
		s.mu.Unlock()
		return nil
	}
	if len(inventory) == 0 {
		call.Status = StatusFailed
		s.calls[callID] = call
		s.closeCallDoneLocked(callID)
		s.activeID = ""
		delete(s.uncertain, callID)
		s.mu.Unlock()
		return s.persistLifecycle(ctx, call, "failed")
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
		s.mu.Unlock()
		return s.persistLifecycle(ctx, call, "")
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) reconcileHangup(ctx context.Context, callID string) error {
	inventory, err := s.provider.ActiveCalls(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	call, ok := s.calls[callID]
	if !ok || s.activeID != callID || !s.hangupUncertain[callID] {
		s.mu.Unlock()
		return nil
	}
	if len(inventory) == 1 && inventoryMatches(inventory[0], call) {
		if call.ProviderCallID == "" {
			call.ProviderCallID = inventory[0].ProviderCallID
		}
		s.calls[callID] = call
		delete(s.hangupUncertain, callID)
		delete(s.requested, callID)
		s.mu.Unlock()
		return nil
	}
	if len(inventory) != 0 {
		s.mu.Unlock()
		return nil
	}
	if call.Status == StatusConnected {
		call.Status = StatusCompleted
	} else {
		call.Status = StatusCanceled
	}
	s.calls[callID] = call
	s.activeID = ""
	s.closeCallDoneLocked(callID)
	delete(s.hangupUncertain, callID)
	s.mu.Unlock()
	return s.persistLifecycle(ctx, call, string(call.Status))
}

func (s *Service) persistLifecycle(ctx context.Context, call Call, reason string) error {
	if s.repository == nil {
		return nil
	}
	if err := s.repository.UpdateLifecycle(ctx, call.CallID, string(call.Status), call.ProviderCallID, reason); err != nil {
		s.setPersistenceError(err)
		return ErrPersistenceFailure
	}
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
	if strings.EqualFold(event.Type, "AUDIO_ERROR") {
		s.recordAudioError(event)
		return
	}
	if strings.EqualFold(event.Class, "call") && (strings.EqualFold(event.Type, "CALL_FAILED") || strings.EqualFold(event.Type, "CALL_CLOSED")) {
		s.recordTerminalDiagnostic(event)
	}
	if !strings.EqualFold(event.Class, "call") {
		return
	}
	s.mu.Lock()
	if s.activeID == "" {
		s.mu.Unlock()
		return
	}
	call := s.calls[s.activeID]
	if call.ProviderCallID == "" {
		if !eventMatchesDestination(event, call.To) {
			s.mu.Unlock()
			return
		}
		call.ProviderCallID = event.CallID
		delete(s.uncertain, call.CallID)
	} else if event.CallID != call.ProviderCallID {
		s.mu.Unlock()
		return
	}
	// Record only normalized event metadata; never log peer URIs, SIP payloads,
	// credentials, or arbitrary provider parameters.
	callID, providerCallID := call.CallID, call.ProviderCallID
	terminalReason := ""
	if strings.HasPrefix(strings.ToUpper(event.Type), "CALL_") {
		if strings.EqualFold(event.Type, "CALL_CLOSED") || strings.EqualFold(event.Type, "CALL_TERMINATED") || strings.EqualFold(event.Type, "CALL_FAILED") {
			terminalReason = safeTerminalReason(event.Param)
		}
		log.Printf("call_timeline api_call_id=%s provider_call_id=%s event=%s at=%s dropped_ctrl_events=%d terminal_reason=%s", callID, providerCallID, safeEventType(event.Type), time.Now().UTC().Format(time.RFC3339Nano), droppedEvents(s.provider), terminalReason)
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
			call.TerminalReason = event.Param
			if call.TerminalReason == "" {
				call.TerminalReason = string(call.Status)
			}
			s.activeID = ""
			s.closeCallDoneLocked(call.CallID)
			delete(s.uncertain, call.CallID)
			delete(s.hangupUncertain, call.CallID)
		}
	default:
		s.mu.Unlock()
		return
	}
	s.calls[call.CallID] = call
	s.mu.Unlock()
	if s.repository != nil {
		reason := ""
		if call.Status.terminal() {
			reason = call.TerminalReason
			if reason == "" {
				reason = string(call.Status)
			}
		}
		if err := s.repository.UpdateLifecycle(s.ctx, call.CallID, string(call.Status), call.ProviderCallID, reason); err != nil {
			s.setPersistenceError(err)
		}
	}
}

// closeCallDoneLocked publishes one terminal transition to call-scoped waiters.
// Callers must hold s.mu.
func (s *Service) closeCallDoneLocked(callID string) {
	if done := s.callDone[callID]; done != nil {
		close(done)
		delete(s.callDone, callID)
	}
}

func droppedEvents(provider control.Provider) uint64 {
	if reporter, ok := provider.(interface{ DroppedEventCount() uint64 }); ok {
		return reporter.DroppedEventCount()
	}
	return 0
}

func safeEventType(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) > 48 {
		return "UNKNOWN"
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "UNKNOWN"
		}
	}
	return value
}

func safeTerminalReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 7 && strings.HasPrefix(value, "sip_") && value[4] >= '4' && value[4] <= '5' && value[5] >= '0' && value[5] <= '9' && value[6] >= '0' && value[6] <= '9' {
		return value
	}
	switch value {
	case "busy", "no_answer", "canceled", "local_hangup", "normal", "transport_error", "unknown", "failed":
		return value
	default:
		return "failed"
	}
}

func (s *Service) setPersistenceError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistenceErr == nil {
		s.persistenceErr = err
	}
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
		switch event.State {
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

// RecordMediaMilestone keeps the first audio observation on each boundary.
// Fixed slots allow temporal comparison with AUDIO_ERROR and terminal events
// without an unbounded event/PCM history or a database hop per audio frame.
func (s *Service) RecordMediaMilestone(callID, stage string) {
	if stage != "gemini_audio" && stage != "media_egress" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.calls[callID]
	if !ok {
		return
	}
	var slot **time.Time
	name := "gemini_audio_first"
	if stage == "gemini_audio" {
		slot = &c.GeminiAudioFirstAt
	} else {
		slot = &c.MediaEgressFirstAt
		name = "media_egress_first"
	}
	if *slot != nil {
		return
	}
	at := time.Now().UTC()
	*slot = &at
	s.calls[callID] = c
	log.Printf("call_media_timeline api_call_id=%s event=%s at=%s", callID, name, at.Format(time.RFC3339Nano))
}

func safeAudioClass(value string) string {
	switch value {
	case "audio_buffer_limit", "frame_protocol", "peer_closed", "socket_io":
		return value
	default:
		return "audio_device"
	}
}

func (s *Service) recordAudioError(event control.Event) {
	class := safeAudioClass(event.Param)
	at := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.calls[s.activeID]
	if !ok || (event.CallID != "" && event.CallID != c.ProviderCallID) {
		log.Printf("call_media_timeline api_call_id=none event=AUDIO_ERROR audio_error_class=%s at=%s", class, at.Format(time.RFC3339Nano))
		return
	}
	if !c.AudioErrorSeen {
		c.AudioErrorSeen = true
		c.AudioErrorAt = &at
		c.AudioErrorClass = class
		s.calls[c.CallID] = c
	}
	// IDs printed here come only from the already-correlated lifecycle, never
	// arbitrary non-call event data. AUDIO_ERROR does not dispatch Hangup.
	log.Printf("call_media_timeline api_call_id=%s provider_call_id=%s event=AUDIO_ERROR audio_error_class=%s at=%s", c.CallID, c.ProviderCallID, class, at.Format(time.RFC3339Nano))
}

func (s *Service) recordTerminalDiagnostic(event control.Event) {
	if event.CallID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range s.calls {
		if c.ProviderCallID != event.CallID {
			continue
		}
		var slot **time.Time
		if strings.EqualFold(event.Type, "CALL_FAILED") {
			slot = &c.CallFailedAt
		} else {
			slot = &c.CallClosedAt
		}
		if *slot != nil {
			return
		}
		at := time.Now().UTC()
		*slot = &at
		s.calls[id] = c
		log.Printf("call_media_timeline api_call_id=%s provider_call_id=%s event=%s at=%s", id, c.ProviderCallID, safeEventType(event.Type), at.Format(time.RFC3339Nano))
		return
	}
}
