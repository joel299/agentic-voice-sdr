package callservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	voicecalldomain "github.com/joel299/agentic-voice-sdr/internal/domain/voicecall"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

type fakeProvider struct {
	mu                sync.Mutex
	registration      control.RegistrationStatus
	registrationErr   error
	dialErr           error
	hangupErr         error
	activeCalls       []control.ActiveCall
	activeCallsErr    error
	dialHook          func(*fakeProvider)
	dialCalls         int
	hangupCalls       int
	dispatchedHangups int
	events            chan control.Event
}

type callRepositoryFake struct {
	mu        sync.Mutex
	created   []voicecalldomain.Call
	updates   []struct{ id, status, providerID, reason string }
	aiUpdates []struct{ id, status, stage, failureClass string }
	err       error
}

func (r *callRepositoryFake) CreateCall(_ context.Context, c voicecalldomain.Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, c)
	return r.err
}
func (r *callRepositoryFake) UpdateLifecycle(_ context.Context, id, status, providerID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, struct{ id, status, providerID, reason string }{id, status, providerID, reason})
	return r.err
}
func (r *callRepositoryFake) UpdateAIRuntimeStatus(_ context.Context, id, status, stage, failureClass string, _ *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aiUpdates = append(r.aiUpdates, struct{ id, status, stage, failureClass string }{id, status, stage, failureClass})
	return r.err
}
func (*callRepositoryFake) GetCall(context.Context, string) (voicecalldomain.Call, error) {
	return voicecalldomain.Call{}, nil
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{registration: control.RegistrationStatus{State: control.RegistrationRegistered}, events: make(chan control.Event, 16)}
}
func (p *fakeProvider) RegistrationStatus(context.Context) (control.RegistrationStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registration, p.registrationErr
}
func (p *fakeProvider) Dial(context.Context, string) (control.CommandResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dialCalls++
	if p.dialHook != nil {
		p.dialHook(p)
	}
	return control.CommandResult{OK: true}, p.dialErr
}
func (p *fakeProvider) Hangup(context.Context) (control.CommandResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hangupCalls++
	if certainty := control.DispatchCertaintyOf(p.hangupErr); certainty != control.DispatchNotDispatched {
		p.dispatchedHangups++
	}
	return control.CommandResult{OK: true}, p.hangupErr
}
func (p *fakeProvider) ActiveCalls(context.Context) ([]control.ActiveCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]control.ActiveCall(nil), p.activeCalls...), p.activeCallsErr
}
func (*fakeProvider) ListCalls(context.Context) (control.CommandResult, error) {
	return control.CommandResult{OK: true}, nil
}
func (p *fakeProvider) Events() <-chan control.Event { return p.events }
func (*fakeProvider) Close() error                   { return nil }
func (p *fakeProvider) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialCalls, p.hangupCalls
}
func (p *fakeProvider) setInventory(calls []control.ActiveCall, err error) {
	p.mu.Lock()
	p.activeCalls = append([]control.ActiveCall(nil), calls...)
	p.activeCallsErr = err
	p.mu.Unlock()
}

func testService(t *testing.T, provider *fakeProvider) *Service {
	t.Helper()
	policy, err := NewAllowlist([]string{"+5567981340687"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(provider, policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestStartNormalizesAllowedDestinationAndCorrelatesProviderCallID(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+55 (67) 98134-0687")
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != StatusDialing || call.To != "+5567981340687" || call.CallID == "" {
		t.Fatalf("unexpected call: %+v", call)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_OUTGOING", CallID: "baresip-42", PeerURI: "sip:+5567981340687@falepaco.example", Direction: "outgoing"}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.ProviderCallID == "baresip-42" })
	got, err := service.Get(call.CallID)
	if err != nil || got.ProviderCallID != "baresip-42" {
		t.Fatalf("provider call id not preserved: %+v, %v", got, err)
	}
	dials, _ := provider.counts()
	if dials != 1 {
		t.Fatalf("Dial calls=%d, want exactly one", dials)
	}
}

func TestWaitCallEndNotifiesMediaSupervisorOnceOnTerminalEvent(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- service.WaitCallEnd(context.Background(), call.CallID) }()
	provider.events <- control.Event{Class: "call", Type: "CALL_OUTGOING", CallID: "baresip-terminal", PeerURI: "sip:+5567981340687@example.test"}
	provider.events <- control.Event{Class: "call", Type: "CALL_CLOSED", CallID: "baresip-terminal", State: control.CallStateCompleted}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("wait for terminal call: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("media supervisor was not notified of CALL_CLOSED")
	}
	if err := service.WaitCallEnd(context.Background(), call.CallID); err != nil {
		t.Fatalf("terminal call should remain observable: %v", err)
	}
}

func TestCallFailedPersistsSanitizedTerminalReason(t *testing.T) {
	provider := newFakeProvider()
	repo := &callRepositoryFake{}
	policy, err := NewAllowlist([]string{"+5567981340687"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithRepository(provider, policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_FAILED", State: control.CallStateFailed, CallID: "baresip-failed", PeerURI: "sip:+5567981340687@example.test", Param: "sip_403"}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusFailed })
	got, err := service.Get(call.CallID)
	if err != nil || got.TerminalReason != "sip_403" {
		t.Fatalf("call=%+v err=%v; want sanitized sip_403", got, err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.updates) == 0 || repo.updates[len(repo.updates)-1].reason != "sip_403" {
		t.Fatalf("persisted updates=%+v; want terminal reason sip_403", repo.updates)
	}
}

func TestAIFailureStatusPersistsWithoutEndingOrHangingUpCall(t *testing.T) {
	provider := newFakeProvider()
	repo := &callRepositoryFake{}
	policy, err := NewAllowlist([]string{"+5567981340687"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithRepository(provider, policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_ESTABLISHED", State: control.CallStateConnected, CallID: "baresip-ai-failure", PeerURI: "sip:+5567981340687@example.test"}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusConnected })
	failedAt := time.Now().UTC()
	if err := service.UpdateAIRuntimeStatus(context.Background(), call.CallID, "failed", "input_transcription_receive", "provider_transport", &failedAt); err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(call.CallID)
	if err != nil || got.Status != StatusConnected || got.AIRuntimeStatus != "failed" || got.AIRuntimeStage != "input_transcription_receive" || got.AIFailureClass != "provider_transport" || got.AIFailureAt == nil || !got.AIFailureAt.Equal(failedAt) {
		t.Fatalf("call after isolated AI failure=%+v err=%v", got, err)
	}
	if _, active := service.ActiveCall(); !active {
		t.Fatal("AI failure terminalized the active SIP call")
	}
	_, hangups := provider.counts()
	if hangups != 0 {
		t.Fatalf("AI failure dispatched %d Hangup commands", hangups)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.aiUpdates) != 1 || repo.aiUpdates[0].status != "failed" || repo.aiUpdates[0].stage != "input_transcription_receive" || repo.aiUpdates[0].failureClass != "provider_transport" {
		t.Fatalf("persisted AI status=%+v", repo.aiUpdates)
	}
}

func TestCallLifecycleUsesRepositoryAndSurfacesWriteFailure(t *testing.T) {
	provider := newFakeProvider()
	repo := &callRepositoryFake{}
	policy, err := NewAllowlist([]string{"+5567981340687"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithRepository(provider, policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	if len(repo.created) != 1 || repo.created[0].Status != "dialing" || repo.created[0].Provider != "baresip" {
		t.Fatalf("create persistence: %+v", repo.created)
	}
	repo.mu.Unlock()
	provider.events <- control.Event{Class: "call", Type: "CALL_ESTABLISHED", State: control.CallStateConnected, CallID: "b-1", PeerURI: "sip:+5567981340687@example.test"}
	waitFor(t, func() bool { repo.mu.Lock(); defer repo.mu.Unlock(); return len(repo.updates) > 0 })
	if _, err := service.Get(call.CallID); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	last := repo.updates[len(repo.updates)-1]
	repo.mu.Unlock()
	if last.status != "connected" || last.providerID != "b-1" {
		t.Fatalf("lifecycle update=%+v", last)
	}
	provider2 := newFakeProvider()
	repo2 := &callRepositoryFake{err: errors.New("database detail")}
	service2, err := NewWithRepository(provider2, policy, repo2)
	if err != nil {
		t.Fatal(err)
	}
	defer service2.Close()
	if _, err := service2.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrPersistenceFailure) {
		t.Fatalf("persistence error=%v", err)
	}
	dials, _ := provider2.counts()
	if dials != 0 {
		t.Fatalf("dialed after failed persistence: %d", dials)
	}
}

func TestUncertainDialReconciliationPersistsTerminalFailure(t *testing.T) {
	provider := newFakeProvider()
	provider.dialErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("dispatch outcome unknown")}
	repo := &callRepositoryFake{}
	policy, err := NewAllowlist([]string{"+5567981340687"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithRepository(provider, policy, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("Start error=%v", err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.updates) != 1 || repo.updates[0].status != "failed" {
		t.Fatalf("reconciled lifecycle was not persisted: %+v", repo.updates)
	}
}

func TestStartFailsClosedForInvalidOrUnlistedDestination(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	for _, tc := range []struct {
		value string
		want  error
	}{
		{"not a phone", ErrInvalidDestination},
		{"+14155550100", ErrDestinationDenied},
	} {
		if _, err := service.Start(context.Background(), tc.value); !errors.Is(err, tc.want) {
			t.Errorf("Start(%q) error=%v, want %v", tc.value, err, tc.want)
		}
	}
	dials, _ := provider.counts()
	if dials != 0 {
		t.Fatalf("invalid destinations caused %d Dial calls", dials)
	}
}

func TestRegistrationGateAndOneActiveCall(t *testing.T) {
	provider := newFakeProvider()
	provider.registration.State = control.RegistrationNotRegistered
	service := testService(t, provider)
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("error=%v, want not registered", err)
	}
	dials, _ := provider.counts()
	if dials != 0 {
		t.Fatalf("unregistered provider caused %d Dial calls", dials)
	}
	provider.registration.State = control.RegistrationRegistered
	first, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrCallActive) {
		t.Fatalf("second call error=%v", err)
	}
	dials, _ = provider.counts()
	if dials != 1 {
		t.Fatalf("Dial calls=%d, want 1", dials)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_OUTGOING", CallID: "baresip-busy", PeerURI: "sip:+5567981340687@falepaco.example"}
	provider.events <- control.Event{Class: "call", Type: "CALL_CLOSED", CallID: "baresip-busy", State: control.CallStateBusy}
	waitFor(t, func() bool { got, _ := service.Get(first.CallID); return got.Status == StatusBusy })
	if _, err := service.Start(context.Background(), "+5567981340687"); err != nil {
		t.Fatalf("new call after terminal state: %v", err)
	}
}

func TestConcurrentStartsIssueAtMostOneDial(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.Start(context.Background(), "+5567981340687"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	dials, _ := provider.counts()
	if successes != 1 || dials != 1 {
		t.Fatalf("successful starts=%d, Dial calls=%d, want exactly one each", successes, dials)
	}
}

func TestHangupTargetsOnlyActiveCallAndAtMostOnce(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Hangup(context.Background(), "another-call"); !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("wrong id error=%v", err)
	}
	if _, err := service.Hangup(context.Background(), call.CallID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Hangup(context.Background(), call.CallID); !errors.Is(err, ErrHangupRequested) {
		t.Fatalf("repeated hangup error=%v", err)
	}
	_, hangups := provider.counts()
	if hangups != 1 {
		t.Fatalf("Hangup calls=%d, want exactly 1", hangups)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_OUTGOING", CallID: "baresip-end", PeerURI: "sip:+5567981340687@falepaco.example"}
	provider.events <- control.Event{Class: "call", Type: "CALL_CLOSED", CallID: "baresip-end", State: control.CallStateCanceled}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusCanceled })
	if _, err := service.Hangup(context.Background(), call.CallID); !errors.Is(err, ErrCallNotActive) {
		t.Fatalf("terminal call hangup error=%v", err)
	}
}

func TestRegistrationProviderErrorIsSafe(t *testing.T) {
	provider := newFakeProvider()
	provider.registrationErr = errors.New("SIP password Authorization: secret")
	service := testService(t, provider)
	if _, err := service.Start(context.Background(), "+5567981340687"); err != ErrProviderFailure {
		t.Fatalf("provider error exposed: %v", err)
	}
}

func TestProviderActiveCallBlocksNewServiceStart(t *testing.T) {
	provider := newFakeProvider()
	provider.activeCalls = []control.ActiveCall{{ProviderCallID: "existing", PeerURI: "sip:+5567981340687@example"}}
	service := testService(t, provider)
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrCallActive) {
		t.Fatalf("Start error=%v, want ErrCallActive", err)
	}
	dials, _ := provider.counts()
	if dials != 0 {
		t.Fatalf("Dial calls=%d, want 0", dials)
	}
}

func TestProviderZeroCallsAllowsStart(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	if _, err := service.Start(context.Background(), "+5567981340687"); err != nil {
		t.Fatal(err)
	}
	dials, _ := provider.counts()
	if dials != 1 {
		t.Fatalf("Dial calls=%d, want 1", dials)
	}
}

func TestAmbiguousDialKeepsGateUntilInventoryProvesNoCall(t *testing.T) {
	provider := newFakeProvider()
	provider.dialErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("response lost")}
	provider.dialHook = func(p *fakeProvider) { p.activeCallsErr = errors.New("inventory unavailable") }
	service := testService(t, provider)
	if _, err := service.Start(context.Background(), "+5567981340687"); err != ErrProviderFailure {
		t.Fatalf("Start error=%v, want safe provider failure", err)
	}
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrCallActive) {
		t.Fatalf("second Start error=%v, want active gate", err)
	}
	dials, _ := provider.counts()
	if dials != 1 {
		t.Fatalf("Dial calls=%d, want 1", dials)
	}
	service.mu.RLock()
	callID := service.activeID
	service.mu.RUnlock()
	provider.setInventory(nil, nil)
	if err := service.Reconcile(context.Background(), callID); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.dialErr = nil
	provider.dialHook = nil
	provider.mu.Unlock()
	if _, err := service.Start(context.Background(), "+5567981340687"); err != nil {
		t.Fatalf("Start after zero-call reconciliation: %v", err)
	}
}

func TestAmbiguousDialMatchingInventoryCorrelatesAndKeepsGate(t *testing.T) {
	provider := newFakeProvider()
	provider.dialErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("response lost")}
	provider.dialHook = func(p *fakeProvider) { p.activeCallsErr = errors.New("inventory unavailable") }
	service := testService(t, provider)
	_, _ = service.Start(context.Background(), "+5567981340687")
	service.mu.RLock()
	callID := service.activeID
	service.mu.RUnlock()
	provider.setInventory([]control.ActiveCall{{ProviderCallID: "baresip-7", PeerURI: "sip:+5567981340687@example", State: control.CallStateRinging}}, nil)
	if err := service.Reconcile(context.Background(), callID); err != nil {
		t.Fatal(err)
	}
	call, _ := service.Get(callID)
	if call.ProviderCallID != "baresip-7" || call.Status != StatusRinging {
		t.Fatalf("call was not reconciled: %+v", call)
	}
	if _, err := service.Start(context.Background(), "+5567981340687"); !errors.Is(err, ErrCallActive) {
		t.Fatalf("Start error=%v, want active gate", err)
	}
	dials, _ := provider.counts()
	if dials != 1 {
		t.Fatalf("Dial calls=%d, want 1", dials)
	}
}

func TestHangupNotDispatchedCanRetry(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	provider.hangupErr = &control.CommandError{Certainty: control.DispatchNotDispatched, Cause: errors.New("disconnected")}
	if _, err := service.Hangup(context.Background(), call.CallID); err != ErrProviderFailure {
		t.Fatalf("first Hangup error=%v", err)
	}
	provider.hangupErr = nil
	if _, err := service.Hangup(context.Background(), call.CallID); err != nil {
		t.Fatalf("retry Hangup error=%v", err)
	}
	provider.mu.Lock()
	attempts, dispatched := provider.hangupCalls, provider.dispatchedHangups
	provider.mu.Unlock()
	if attempts != 2 || dispatched != 1 {
		t.Fatalf("hangup attempts=%d dispatched=%d, want 2 and 1", attempts, dispatched)
	}
}

func TestAmbiguousHangupDoesNotDuplicateAndReconciles(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_ESTABLISHED", CallID: "baresip-established", PeerURI: "sip:+5567981340687@example"}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusConnected })
	provider.hangupErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("response lost")}
	if _, err := service.Hangup(context.Background(), call.CallID); err != ErrProviderFailure {
		t.Fatalf("Hangup error=%v", err)
	}
	_, sent := provider.counts()
	if sent != 1 {
		t.Fatalf("first ambiguous Hangup calls=%d, want 1", sent)
	}
	provider.setInventory([]control.ActiveCall{{ProviderCallID: "baresip-established", PeerURI: "sip:+5567981340687@example", State: control.CallStateConnected}}, nil)
	if _, err := service.Hangup(context.Background(), call.CallID); !errors.Is(err, ErrHangupRequested) {
		t.Fatalf("reconciliation Hangup error=%v, want safe retry conflict", err)
	}
	_, sent = provider.counts()
	if sent != 1 {
		t.Fatalf("reconciliation sent duplicate Hangup; calls=%d", sent)
	}
	provider.hangupErr = nil
	if _, err := service.Hangup(context.Background(), call.CallID); err != nil {
		t.Fatalf("explicit retry Hangup=%v", err)
	}
	if _, err := service.Hangup(context.Background(), call.CallID); !errors.Is(err, ErrHangupRequested) {
		t.Fatalf("third Hangup error=%v, want one-shot guard", err)
	}
	_, sent = provider.counts()
	if sent != 2 {
		t.Fatalf("dispatched Hangup calls=%d, want exactly 2", sent)
	}
}

func TestAmbiguousHangupZeroInventoryMarksTerminal(t *testing.T) {
	provider := newFakeProvider()
	service := testService(t, provider)
	call, err := service.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	provider.events <- control.Event{Class: "call", Type: "CALL_ESTABLISHED", CallID: "baresip-zero", PeerURI: "sip:+5567981340687@example"}
	waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusConnected })
	provider.hangupErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("response lost")}
	if _, err := service.Hangup(context.Background(), call.CallID); err != ErrProviderFailure {
		t.Fatalf("Hangup error=%v", err)
	}
	provider.setInventory(nil, nil)
	if _, err := service.Hangup(context.Background(), call.CallID); err != nil {
		t.Fatalf("zero-call reconciliation=%v", err)
	}
	got, _ := service.Get(call.CallID)
	if got.Status != StatusCompleted {
		t.Fatalf("status=%s, want completed", got.Status)
	}
	_, sent := provider.counts()
	if sent != 1 {
		t.Fatalf("Hangup calls=%d, want 1", sent)
	}
}

func TestAmbiguousHangupUnknownOrDifferentInventoryStaysFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		inventory []control.ActiveCall
		err       error
	}{
		{name: "different call", inventory: []control.ActiveCall{{ProviderCallID: "other", PeerURI: "sip:+15550001111@example"}}},
		{name: "provider ID mismatch forbids URI fallback", inventory: []control.ActiveCall{{ProviderCallID: "other", PeerURI: "sip:+5567981340687@example"}}},
		{name: "opaque call", inventory: []control.ActiveCall{{}}},
		{name: "inventory query failure", err: errors.New("inventory unavailable")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeProvider()
			service := testService(t, provider)
			call, err := service.Start(context.Background(), "+5567981340687")
			if err != nil {
				t.Fatal(err)
			}
			provider.events <- control.Event{Class: "call", Type: "CALL_ESTABLISHED", CallID: "baresip-fail-closed", PeerURI: "sip:+5567981340687@example"}
			waitFor(t, func() bool { got, _ := service.Get(call.CallID); return got.Status == StatusConnected })
			provider.hangupErr = &control.CommandError{Certainty: control.DispatchMaybeDispatched, Cause: errors.New("response lost")}
			if _, err := service.Hangup(context.Background(), call.CallID); err != ErrProviderFailure {
				t.Fatalf("first Hangup error=%v", err)
			}
			provider.setInventory(tc.inventory, tc.err)
			if _, err := service.Hangup(context.Background(), call.CallID); err == nil {
				t.Fatal("unknown inventory incorrectly allowed retry")
			}
			provider.mu.Lock()
			commands := provider.hangupCalls
			provider.mu.Unlock()
			if commands != 1 {
				t.Fatalf("Hangup calls=%d, want 1", commands)
			}
		})
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestAudioErrorAndTerminalTimelineIsCorrelatedSafeAndBounded(t *testing.T) {
	p := newFakeProvider()
	s := testService(t, p)
	c, err := s.Start(context.Background(), "+5567981340687")
	if err != nil {
		t.Fatal(err)
	}
	s.applyEvent(control.Event{Class: "call", Type: "CALL_OUTGOING", CallID: "media-call"})
	s.applyEvent(control.Event{Class: "call", Type: "CALL_ESTABLISHED", CallID: "media-call"})
	s.RecordMediaMilestone(c.CallID, "gemini_audio")
	s.RecordMediaMilestone(c.CallID, "media_egress")
	before, _ := s.Get(c.CallID)
	s.RecordMediaMilestone(c.CallID, "gemini_audio")
	s.applyEvent(control.Event{Class: "other", Type: "AUDIO_ERROR", CallID: "wrong-call", Param: "frame_protocol"})
	got, _ := s.Get(c.CallID)
	if got.AudioErrorSeen {
		t.Fatal("cross-call audio error leaked")
	}
	s.applyEvent(control.Event{Class: "other", Type: "AUDIO_ERROR", CallID: "media-call", Param: "password secret"})
	got, _ = s.Get(c.CallID)
	if !got.AudioErrorSeen || got.AudioErrorClass != "audio_device" || got.Status != StatusConnected {
		t.Fatalf("audio diagnostic = %+v", got)
	}
	s.applyEvent(control.Event{Class: "call", Type: "CALL_FAILED", CallID: "media-call", State: control.CallStateFailed, Param: "failed"})
	s.applyEvent(control.Event{Class: "call", Type: "CALL_CLOSED", CallID: "media-call", State: control.CallStateFailed, Param: "unknown"})
	got, _ = s.Get(c.CallID)
	if got.GeminiAudioFirstAt != before.GeminiAudioFirstAt || got.CallClosedAt == nil || got.CallFailedAt == nil {
		t.Fatal("fixed first-event slots missing/overwritten")
	}
	if got.MediaEgressFirstAt.Before(*got.GeminiAudioFirstAt) || got.AudioErrorAt.Before(*got.MediaEgressFirstAt) || got.CallFailedAt.Before(*got.AudioErrorAt) || got.CallClosedAt.Before(*got.CallFailedAt) {
		t.Fatal("audio -> error -> terminal order lost")
	}
	_, hangups := p.counts()
	if hangups != 0 {
		t.Fatal("diagnostic dispatched Hangup")
	}
}
