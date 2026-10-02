package callservice

import (
	"context"
	"errors"
	"testing"
)

func TestEmptyAllowlistIsConfigurationFailureWithoutDial(t *testing.T) {
	p, _ := NewAllowlist(nil)
	provider := newFakeProvider()
	s, _ := New(provider, p)
	defer s.Close()
	_, e := s.Start(context.Background(), "+5567981340687")
	if !errors.Is(e, ErrDestinationPolicyNotConfigured) {
		t.Fatalf("error=%v", e)
	}
	if len(s.AllowedDestinations()) != 0 {
		t.Fatal("empty policy ready")
	}
	if d, _ := provider.counts(); d != 0 {
		t.Fatal("dial attempted")
	}
}
func TestAuthorizedAllowlistCanonicalAndFormatted(t *testing.T) {
	p, _ := NewAllowlist([]string{"+5567981340687", "+55 (67) 98134-0687"})
	for _, v := range []string{"+5567981340687", "+55 (67) 98134-0687"} {
		n, e := p.Normalize(v)
		if e != nil || n != "+5567981340687" || !p.Allows(n) {
			t.Fatal("authorized destination lost")
		}
	}
	if len(p.Destinations()) != 1 {
		t.Fatal("normalization did not deduplicate")
	}
	copy := p.Destinations()
	copy[0] = "changed"
	if !p.Allows("+5567981340687") {
		t.Fatal("mutable policy")
	}
}
func TestDifferentDestinationDeniedWithoutDial(t *testing.T) {
	provider := newFakeProvider()
	s := testService(t, provider)
	_, e := s.Start(context.Background(), "+5567981340688")
	if !errors.Is(e, ErrDestinationDenied) {
		t.Fatalf("error=%v", e)
	}
	if d, _ := provider.counts(); d != 0 {
		t.Fatal("dial attempted")
	}
}
