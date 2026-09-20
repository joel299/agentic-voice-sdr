package httpapi

import (
	"context"
	"strings"
	"testing"
)

type sequenceSIPResolver struct {
	answers [][]string
	calls   int
}

func (r *sequenceSIPResolver) LookupHost(context.Context, string) ([]string, error) {
	answer := r.answers[r.calls]
	r.calls++
	return answer, nil
}

func TestSafeSIPNetworkDialerRejectsPrivateResolutionAtDialTime(t *testing.T) {
	resolver := &sequenceSIPResolver{answers: [][]string{{"93.184.216.34"}, {"127.0.0.1"}}}
	dialer := newSafeSIPNetworkDialer()
	dialer.resolver = resolver
	if _, err := dialer.LookupHost(context.Background(), "sip.example.test"); err != nil {
		t.Fatal(err)
	}
	_, err := dialer.DialContext(context.Background(), "tcp", "sip.example.test:5060")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected dial-time private resolution rejection, got %v", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected initial and dial-time resolution, got %d calls", resolver.calls)
	}
}

func TestSafeSIPNetworkDialerRejectsPrivateInitialResolution(t *testing.T) {
	resolver := &sequenceSIPResolver{answers: [][]string{{"10.0.0.2"}}}
	dialer := newSafeSIPNetworkDialer()
	dialer.resolver = resolver
	if _, err := dialer.LookupHost(context.Background(), "sip.example.test"); err == nil {
		t.Fatal("private initial resolution accepted")
	}
}

func TestSafeSIPNetworkDialerAcceptsPublicResolution(t *testing.T) {
	resolver := &sequenceSIPResolver{answers: [][]string{{"93.184.216.34"}}}
	dialer := newSafeSIPNetworkDialer()
	dialer.resolver = resolver
	addrs, err := dialer.LookupHost(context.Background(), "sip.example.test")
	if err != nil || len(addrs) != 1 || addrs[0] != "93.184.216.34" {
		t.Fatalf("public resolution rejected: addrs=%v err=%v", addrs, err)
	}
}
