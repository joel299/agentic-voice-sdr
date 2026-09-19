package sip

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// TransportType represents the SIP transport protocol.
type TransportType string

const (
	TransportUDP TransportType = "udp"
	TransportTCP TransportType = "tcp"
	TransportTLS TransportType = "tls"
)

// AuthType represents the authentication method used by the trunk.
type AuthType string

const (
	AuthUserPass AuthType = "userpass"
	AuthIP       AuthType = "ip"
	AuthNone     AuthType = "none"
)

// LifecycleStatus represents the operational status of the SIP trunk.
type LifecycleStatus string

const (
	StatusConfigured         LifecycleStatus = "CONFIGURED"
	StatusValidating         LifecycleStatus = "VALIDATING"
	StatusRegistered         LifecycleStatus = "REGISTERED"
	StatusReady              LifecycleStatus = "READY"
	StatusDNSError           LifecycleStatus = "DNS_ERROR"
	StatusConnectionError    LifecycleStatus = "CONNECTION_ERROR"
	StatusAuthError          LifecycleStatus = "AUTH_ERROR"
	StatusRegistrationFailed LifecycleStatus = "REGISTRATION_FAILED"
	StatusDisabled           LifecycleStatus = "DISABLED"
)

// TrunkConfig defines the provider-agnostic SIP trunk configuration.
type TrunkConfig struct {
	Provider             string        `json:"provider"`
	Name                 string        `json:"name"`
	Host                 string        `json:"host"`
	Port                 int           `json:"port"`
	Transport            TransportType `json:"transport"`
	Registrar            string        `json:"registrar,omitempty"`
	OutboundProxy        string        `json:"outbound_proxy,omitempty"`
	AuthType             AuthType      `json:"auth_type"`
	AuthUsername         string        `json:"auth_username,omitempty"`
	Secret               string        `json:"secret,omitempty"`
	Realm                string        `json:"realm,omitempty"`
	FromUser             string        `json:"from_user,omitempty"`
	FromDomain           string        `json:"from_domain,omitempty"`
	CallerID             string        `json:"caller_id,omitempty"`
	Codecs               []string      `json:"codecs,omitempty"`
	RegistrationRequired bool          `json:"registration_required"`
	Enabled              bool          `json:"enabled"`
}

// String implements fmt.Stringer ensuring Secret is strictly masked.
func (c TrunkConfig) String() string {
	maskedSecret := ""
	if c.Secret != "" {
		maskedSecret = "[REDACTED]"
	}
	return fmt.Sprintf("TrunkConfig{Name: %q, Provider: %q, Host: %q, Port: %d, Transport: %q, AuthType: %q, Username: %q, Secret: %q, Enabled: %t}",
		c.Name, c.Provider, c.Host, c.Port, c.Transport, c.AuthType, c.AuthUsername, maskedSecret, c.Enabled)
}

// GoString implements fmt.GoStringer ensuring Secret is strictly masked in %#v formats.
func (c TrunkConfig) GoString() string {
	return c.String()
}

// Redacted returns a shallow copy with Secret masked for logging/tracing.
func (c TrunkConfig) Redacted() TrunkConfig {
	copyCfg := c
	if copyCfg.Secret != "" {
		copyCfg.Secret = "*****"
	}
	return copyCfg
}

// Validate checks the configuration for semantic correctness.
func (c *TrunkConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("trunk name is required")
	}

	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("trunk host is required")
	}

	if c.Port <= 0 {
		c.Port = 5060
	} else if c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", c.Port)
	}

	c.Transport = TransportType(strings.ToLower(string(c.Transport)))
	if c.Transport == "" {
		c.Transport = TransportUDP
	}

	switch c.Transport {
	case TransportUDP, TransportTCP, TransportTLS:
	default:
		return fmt.Errorf("unsupported transport %q: must be udp, tcp, or tls", c.Transport)
	}

	c.AuthType = AuthType(strings.ToLower(string(c.AuthType)))
	if c.AuthType == "" {
		if c.AuthUsername != "" || c.Secret != "" {
			c.AuthType = AuthUserPass
		} else {
			c.AuthType = AuthIP
		}
	}

	switch c.AuthType {
	case AuthUserPass:
		if strings.TrimSpace(c.AuthUsername) == "" {
			return fmt.Errorf("auth_username is required for userpass authentication")
		}
		if strings.TrimSpace(c.Secret) == "" {
			return fmt.Errorf("secret is required for userpass authentication")
		}
	case AuthIP, AuthNone:
	default:
		return fmt.Errorf("unsupported auth_type %q: must be userpass, ip, or none", c.AuthType)
	}

	if c.RegistrationRequired && c.AuthType == AuthUserPass {
		if strings.TrimSpace(c.AuthUsername) == "" || strings.TrimSpace(c.Secret) == "" {
			return fmt.Errorf("username and secret are required when registration_required is true")
		}
	}

	if len(c.Codecs) == 0 {
		c.Codecs = []string{"ulaw", "alaw"}
	}

	return nil
}

// StatusReport details the live status of the SIP trunk.
type StatusReport struct {
	TrunkName         string          `json:"trunk_name"`
	Provider          string          `json:"provider"`
	Status            LifecycleStatus `json:"status"`
	LastError         string          `json:"last_error,omitempty"`
	AsteriskHealthy   bool            `json:"asterisk_healthy"`
	EndpointActive    bool            `json:"endpoint_active"`
	RegistrationState string          `json:"registration_state,omitempty"`
	LastValidatedAt   time.Time       `json:"last_validated_at"`
}

// CheckHostDNS performs DNS resolution check for target host.
func CheckHostDNS(host string) ([]string, error) {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("dns resolution failed for host %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("dns returned no addresses for host %s", host)
	}
	return addrs, nil
}
