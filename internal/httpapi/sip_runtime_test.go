package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
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

func TestSIPDestinationPolicyProtectsAllAsteriskDestinations(t *testing.T) {
	tests := []struct {
		name string
		cfg  func(*sip.TrunkConfig)
	}{
		{"host", func(c *sip.TrunkConfig) { c.Host = "10.0.0.10" }},
		{"registrar", func(c *sip.TrunkConfig) { c.Registrar = "127.0.0.1" }},
		{"outbound_proxy", func(c *sip.TrunkConfig) { c.OutboundProxy = "169.254.1.1:5060" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sip.TrunkConfig{Host: "sip.example.test", Registrar: "reg.example.test", OutboundProxy: "proxy.example.test:5060"}
			tt.cfg(&cfg)
			policy := &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
			policy.dialer.resolver = &sequenceSIPResolver{answers: [][]string{{"93.184.216.34"}, {"93.184.216.34"}}}
			if _, err := policy.PinConfig(context.Background(), cfg); err == nil {
				t.Fatal("reserved destination accepted")
			}
		})
	}
}

func TestSIPDestinationPolicyPinsOperationalDestination(t *testing.T) {
	resolver := &sequenceSIPResolver{answers: [][]string{{"93.184.216.34"}, {"127.0.0.1"}}}
	policy := &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
	policy.dialer.resolver = resolver
	cfg := sip.TrunkConfig{Host: "rebind.example.test"}
	pinned, err := policy.PinConfig(context.Background(), cfg)
	if err != nil || pinned.Host != "93.184.216.34" {
		t.Fatalf("pinning failed: cfg=%#v err=%v", pinned, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("expected one resolution and no Asterisk-side hostname re-resolution, got %d", resolver.calls)
	}
}

func TestSIPDestinationPolicyRejectsPrivateAndMixedDNS(t *testing.T) {
	tests := []struct {
		name    string
		answers [][]string
		mutate  func(*sip.TrunkConfig)
	}{
		{"host private", [][]string{{"10.0.0.10"}}, func(c *sip.TrunkConfig) { c.Host = "host.example" }},
		{"registrar private", [][]string{{"93.184.216.34"}, {"127.0.0.1"}}, func(c *sip.TrunkConfig) { c.Registrar = "reg.example" }},
		{"outbound proxy private", [][]string{{"93.184.216.34"}, {"169.254.1.1"}}, func(c *sip.TrunkConfig) { c.OutboundProxy = "proxy.example:5060" }},
		{"host mixed", [][]string{{"93.184.216.34", "10.0.0.10"}}, func(c *sip.TrunkConfig) { c.Host = "mixed.example" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sip.TrunkConfig{Host: "sip.example.test"}
			tt.mutate(&cfg)
			policy := &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
			policy.dialer.resolver = &sequenceSIPResolver{answers: tt.answers}
			if _, err := policy.PinConfig(context.Background(), cfg); err == nil {
				t.Fatal("unsafe DNS result accepted")
			}
		})
	}
}
