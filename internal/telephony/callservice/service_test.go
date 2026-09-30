package callservice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
