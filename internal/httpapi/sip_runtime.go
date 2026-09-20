package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

type sipHostResolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

type defaultSIPHostResolver struct{}

func (defaultSIPHostResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return (&net.Resolver{}).LookupHost(ctx, host)
}

type safeSIPNetworkDialer struct {
	resolver sipHostResolver
	dialer   net.Dialer
}

type sipDestinationPolicy struct {
	dialer *safeSIPNetworkDialer
}

func newSIPDestinationPolicy() *sipDestinationPolicy {
	return &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
}

// PinConfig resolves every address that Asterisk may use and replaces hostnames
// with the validated IP. This removes the Go-validation/Asterisk-resolution TOCTOU.
func (p *sipDestinationPolicy) PinConfig(ctx context.Context, cfg sip.TrunkConfig) (sip.TrunkConfig, error) {
	var err error
	if cfg.Host, err = p.pinHost(ctx, cfg.Host); err != nil {
		return sip.TrunkConfig{}, fmt.Errorf("host destination rejected: %w", err)
	}
	if cfg.Registrar != "" {
		if cfg.Registrar, err = p.pinHost(ctx, cfg.Registrar); err != nil {
			return sip.TrunkConfig{}, fmt.Errorf("registrar destination rejected: %w", err)
		}
	}
	if cfg.OutboundProxy != "" {
		if cfg.OutboundProxy, err = p.pinHostPort(ctx, cfg.OutboundProxy); err != nil {
			return sip.TrunkConfig{}, fmt.Errorf("outbound proxy destination rejected: %w", err)
		}
	}
	return cfg, nil
}

func (p *sipDestinationPolicy) pinHost(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil && forbiddenSIPDestination(ip) {
		return "", fmt.Errorf("literal destination is forbidden")
	}
	addrs, err := p.dialer.LookupHost(ctx, host)
	if err != nil {
		return "", err
	}
	return addrs[0], nil
}

func (p *sipDestinationPolicy) pinHostPort(ctx context.Context, value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return p.pinHost(ctx, value)
	}
	ip, err := p.pinHost(ctx, host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip, port), nil
}

func newSafeSIPNetworkDialer() *safeSIPNetworkDialer {
	return &safeSIPNetworkDialer{resolver: defaultSIPHostResolver{}, dialer: net.Dialer{Timeout: 5 * time.Second}}
}

func (d *safeSIPNetworkDialer) LookupHost(ctx context.Context, host string) ([]string, error) {
	addrs, err := d.resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if forbiddenSIPDestination(net.ParseIP(addr)) {
			return nil, fmt.Errorf("SIP destination resolves to a forbidden address")
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("SIP destination resolved to no addresses")
	}
	return addrs, nil
}

func (d *safeSIPNetworkDialer) resolveAddress(ctx context.Context, address string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", "", fmt.Errorf("invalid SIP destination %q", address)
	}
	addrs, err := d.LookupHost(ctx, host)
	if err != nil {
		return "", "", err
	}
	return net.JoinHostPort(addrs[0], port), host, nil
}

func (d *safeSIPNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	resolved, _, err := d.resolveAddress(ctx, address)
	if err != nil {
		return nil, err
	}
	return d.dialer.DialContext(ctx, network, resolved)
}

func (d *safeSIPNetworkDialer) DialTLSContext(ctx context.Context, network, address string) (net.Conn, error) {
	resolved, serverName, err := d.resolveAddress(ctx, address)
	if err != nil {
		return nil, err
	}
	if network == "udp" {
		network = "tcp"
	}
	dialer := tls.Dialer{NetDialer: &d.dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}}
	return dialer.DialContext(ctx, network, resolved)
}

func forbiddenSIPDestination(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
