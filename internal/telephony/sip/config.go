package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

var ErrBoundaryUnavailable = errors.New("sip configuration boundary unavailable")

type Auth struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

type Config struct {
	Provider      string   `json:"provider"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Transport     string   `json:"transport"`
	Registrar     string   `json:"registrar,omitempty"`
	OutboundProxy string   `json:"outbound_proxy,omitempty"`
	Auth          Auth     `json:"auth"`
	FromUser      string   `json:"from_user,omitempty"`
	FromDomain    string   `json:"from_domain,omitempty"`
	CallerID      string   `json:"caller_id,omitempty"`
	Codecs        []string `json:"codecs,omitempty"`
	Enabled       bool     `json:"enabled"`
}

type SafeConfig struct {
	Provider      string   `json:"provider"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Transport     string   `json:"transport"`
	Registrar     string   `json:"registrar,omitempty"`
	OutboundProxy string   `json:"outbound_proxy,omitempty"`
	Auth          SafeAuth `json:"auth"`
	FromUser      string   `json:"from_user,omitempty"`
	FromDomain    string   `json:"from_domain,omitempty"`
	CallerID      string   `json:"caller_id,omitempty"`
	Codecs        []string `json:"codecs,omitempty"`
	Enabled       bool     `json:"enabled"`
}

type SafeAuth struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.Name) == "" {
		return errors.New("provider and name are required")
	}
	if net.ParseIP(c.Host) == nil && strings.TrimSpace(c.Host) == "" {
		return errors.New("host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	transport := strings.ToLower(strings.TrimSpace(c.Transport))
	if transport != "udp" && transport != "tcp" && transport != "tls" {
		return errors.New("transport must be udp, tcp, or tls")
	}
	if strings.TrimSpace(c.Auth.Type) == "" {
		return errors.New("auth.type is required")
	}
	if c.Auth.Type == "password" && (strings.TrimSpace(c.Auth.Username) == "" || c.Auth.Secret == "") {
		return errors.New("password auth requires username and secret")
	}
	if c.Auth.Type != "password" && strings.TrimSpace(c.Auth.Username) == "" {
		return errors.New("auth.username is required")
	}
	return nil
}

func (c Config) SafeView() SafeConfig {
	return SafeConfig{Provider: c.Provider, Name: c.Name, Host: c.Host, Port: c.Port, Transport: c.Transport, Registrar: c.Registrar, OutboundProxy: c.OutboundProxy, Auth: SafeAuth{Type: c.Auth.Type, Username: c.Auth.Username, Realm: c.Auth.Realm}, FromUser: c.FromUser, FromDomain: c.FromDomain, CallerID: c.CallerID, Codecs: append([]string(nil), c.Codecs...), Enabled: c.Enabled}
}

type Configurator interface {
	Configure(context.Context, Config) error
}
type UnavailableConfigurator struct{}

func (UnavailableConfigurator) Configure(context.Context, Config) error {
	return ErrBoundaryUnavailable
}

type MemoryConfigurator struct {
	Configured bool
	Last       SafeConfig
}

func (m *MemoryConfigurator) Configure(_ context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validate SIP config: %w", err)
	}
	m.Configured = true
	m.Last = c.SafeView()
	return nil
}
