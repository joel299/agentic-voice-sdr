package sip

import (
	"fmt"
	"net"
	"regexp"
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

// Allowlist regex for trunk names to strictly prevent path traversal and injection.
var trunkNameRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

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

// Clone returns a deep copy of TrunkConfig with cloned Codecs slice.
func (c TrunkConfig) Clone() TrunkConfig {
	copyCfg := c
	if c.Codecs != nil {
		copyCfg.Codecs = make([]string, len(c.Codecs))
		copy(copyCfg.Codecs, c.Codecs)
	}
	return copyCfg
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

// Redacted returns a deep copy with Secret masked for logging/tracing.
func (c TrunkConfig) Redacted() TrunkConfig {
	copyCfg := c.Clone()
	if copyCfg.Secret != "" {
		copyCfg.Secret = "*****"
	}
	return copyCfg
}

// hasInjectionChars checks if string contains CR, LF, or section injection characters.
func hasInjectionChars(s string) bool {
	return strings.ContainsAny(s, "\r\n[]")
}

// Validate checks the configuration for semantic correctness and injection/traversal safety.
func (c *TrunkConfig) Validate() error {
	trimmedName := strings.TrimSpace(c.Name)
	if trimmedName == "" {
		return fmt.Errorf("trunk name is required")
	}

	if !trunkNameRegex.MatchString(c.Name) {
		return fmt.Errorf("invalid trunk name %q: violates allowlist format (must match ^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$ and contain no path traversal)", c.Name)
	}

	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("trunk host is required")
	}

	// Injection check for all user-supplied string fields
	stringFields := map[string]string{
		"name":           c.Name,
		"provider":       c.Provider,
		"host":           c.Host,
		"registrar":      c.Registrar,
		"outbound_proxy": c.OutboundProxy,
		"auth_username":  c.AuthUsername,
		"secret":         c.Secret,
		"realm":          c.Realm,
		"from_user":      c.FromUser,
		"from_domain":    c.FromDomain,
		"caller_id":      c.CallerID,
	}

	for fieldName, val := range stringFields {
		if hasInjectionChars(val) {
			return fmt.Errorf("security violation: field %s contains invalid line break or injection characters", fieldName)
		}
	}

	for _, codec := range c.Codecs {
		if hasInjectionChars(codec) {
			return fmt.Errorf("security violation: codec %q contains invalid injection characters", codec)
		}
	}

	if c.Port < 0 {
		return fmt.Errorf("invalid port %d: port cannot be negative", c.Port)
	} else if c.Port == 0 {
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

	if c.RegistrationRequired {
		regIdentity := strings.TrimSpace(c.AuthUsername)
		if regIdentity == "" {
			regIdentity = strings.TrimSpace(c.FromUser)
		}
		if regIdentity == "" {
			return fmt.Errorf("registration identity required: auth_username or from_user must be specified when registration_required is true")
		}
	}

	if len(c.Codecs) == 0 {
		c.Codecs = []string{"ulaw", "alaw"}
	} else {
		// Clone codecs array to prevent caller mutation
		cloned := make([]string, len(c.Codecs))
		copy(cloned, c.Codecs)
		c.Codecs = cloned
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
