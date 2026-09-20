package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

var errSIPBoundaryUnavailable = errors.New("sip configuration boundary unavailable")
var errSIPCanonicalValidation = errors.New("invalid canonical SIP configuration")

type SIPAuthRequest struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

type SIPConfigRequest struct {
	Provider             string         `json:"provider"`
	Name                 string         `json:"name"`
	Host                 string         `json:"host"`
	Port                 int            `json:"port"`
	Transport            string         `json:"transport"`
	Registrar            string         `json:"registrar,omitempty"`
	OutboundProxy        string         `json:"outbound_proxy,omitempty"`
	Auth                 SIPAuthRequest `json:"auth"`
	FromUser             string         `json:"from_user,omitempty"`
	FromDomain           string         `json:"from_domain,omitempty"`
	CallerID             string         `json:"caller_id,omitempty"`
	Codecs               []string       `json:"codecs,omitempty"`
	RegistrationRequired bool           `json:"registration_required"`
	Enabled              bool           `json:"enabled"`
}

type SIPSafeResponse struct {
	Provider             string      `json:"provider"`
	Name                 string      `json:"name"`
	Host                 string      `json:"host"`
	Port                 int         `json:"port"`
	Transport            string      `json:"transport"`
	Registrar            string      `json:"registrar,omitempty"`
	OutboundProxy        string      `json:"outbound_proxy,omitempty"`
	Auth                 SIPSafeAuth `json:"auth"`
	FromUser             string      `json:"from_user,omitempty"`
	FromDomain           string      `json:"from_domain,omitempty"`
	CallerID             string      `json:"caller_id,omitempty"`
	Codecs               []string    `json:"codecs,omitempty"`
	RegistrationRequired bool        `json:"registration_required"`
	Enabled              bool        `json:"enabled"`
}

type SIPSafeAuth struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

type SIPConfigurator interface {
	Configure(context.Context, SIPConfigRequest) error
}

// CanonicalSIPConfigurator maps HTTP DTOs into the canonical SIP manager.
type CanonicalSIPConfigurator struct {
	manager interface {
		ApplyTrunk(context.Context, sip.TrunkConfig) (sip.StatusReport, error)
	}
	policy *sipDestinationPolicy
}

func NewCanonicalSIPConfigurator(manager interface {
	ApplyTrunk(context.Context, sip.TrunkConfig) (sip.StatusReport, error)
}) (*CanonicalSIPConfigurator, error) {
	if manager == nil {
		return nil, errSIPBoundaryUnavailable
	}
	return &CanonicalSIPConfigurator{manager: manager}, nil
}

func newCanonicalSIPConfiguratorWithPolicy(manager interface {
	ApplyTrunk(context.Context, sip.TrunkConfig) (sip.StatusReport, error)
}, policy *sipDestinationPolicy) (*CanonicalSIPConfigurator, error) {
	configurator, err := NewCanonicalSIPConfigurator(manager)
	if err != nil {
		return nil, err
	}
	configurator.policy = policy
	return configurator, nil
}

func (c *CanonicalSIPConfigurator) Configure(ctx context.Context, request SIPConfigRequest) error {
	canonical, err := request.ToCanonical()
	if err != nil {
		return fmt.Errorf("%w: %v", errSIPCanonicalValidation, err)
	}
	if c.policy != nil {
		canonical, err = c.policy.PinConfig(ctx, canonical)
		if err != nil {
			return fmt.Errorf("%w: %v", errSIPCanonicalValidation, err)
		}
	}
	_, err = c.manager.ApplyTrunk(ctx, canonical)
	return err
}

type unavailableSIPConfigurator struct{}

func (unavailableSIPConfigurator) Configure(context.Context, SIPConfigRequest) error {
	return errSIPBoundaryUnavailable
}

func (c SIPConfigRequest) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Name) == "" {
		return errors.New("provider and name are required")
	}
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, " \t\r\n") {
		return errors.New("host is required and must not contain whitespace")
	}
	if ip := net.ParseIP(c.Host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast()) {
		return errors.New("host must not be an internal address")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch strings.ToLower(strings.TrimSpace(c.Transport)) {
	case "udp", "tcp", "tls":
	default:
		return errors.New("transport must be udp, tcp, or tls")
	}
	switch strings.ToLower(strings.TrimSpace(c.Auth.Type)) {
	case "userpass":
		if strings.TrimSpace(c.Auth.Username) == "" || c.Auth.Secret == "" {
			return errors.New("userpass auth requires username and secret")
		}
	case "ip", "none":
		if c.Auth.Secret != "" {
			return errors.New("secret is not allowed for this auth type")
		}
	default:
		return fmt.Errorf("auth.type must be userpass, ip, or none")
	}
	return nil
}
func (c SIPConfigRequest) SafeView() SIPSafeResponse {
	return SIPSafeResponse{Provider: c.Provider, Name: c.Name, Host: c.Host, Port: c.Port, Transport: c.Transport, Registrar: c.Registrar, OutboundProxy: c.OutboundProxy, Auth: SIPSafeAuth{Type: c.Auth.Type, Username: c.Auth.Username, Realm: c.Auth.Realm}, FromUser: c.FromUser, FromDomain: c.FromDomain, CallerID: c.CallerID, Codecs: append([]string(nil), c.Codecs...), RegistrationRequired: c.RegistrationRequired, Enabled: c.Enabled}
}

// ToCanonical performs the explicit HTTP DTO to canonical sip.TrunkConfig mapping.
func (c SIPConfigRequest) ToCanonical() (sip.TrunkConfig, error) {
	if err := c.Validate(); err != nil {
		return sip.TrunkConfig{}, err
	}
	canonical := sip.TrunkConfig{
		Provider: c.Provider, Name: c.Name, Host: c.Host, Port: c.Port,
		Transport: sip.TransportType(strings.ToLower(strings.TrimSpace(c.Transport))),
		Registrar: c.Registrar, OutboundProxy: c.OutboundProxy,
		AuthType:     sip.AuthType(strings.ToLower(strings.TrimSpace(c.Auth.Type))),
		AuthUsername: c.Auth.Username, Secret: c.Auth.Secret, Realm: c.Auth.Realm,
		FromUser: c.FromUser, FromDomain: c.FromDomain, CallerID: c.CallerID,
		Codecs: append([]string(nil), c.Codecs...), RegistrationRequired: c.RegistrationRequired,
		Enabled: c.Enabled,
	}
	if err := canonical.Validate(); err != nil {
		return sip.TrunkConfig{}, err
	}
	return canonical, nil
}
