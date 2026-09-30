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
	mu              sync.Mutex
	registration    control.RegistrationStatus
	registrationErr error
	dialErr         error
	hangupErr       error
	dialCalls       int
	hangupCalls     int
	events          chan control.Event
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
	return control.CommandResult{OK: true}, p.dialErr
}
func (p *fakeProvider) Hangup(context.Context) (control.CommandResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hangupCalls++
	return control.CommandResult{OK: true}, p.hangupErr
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
