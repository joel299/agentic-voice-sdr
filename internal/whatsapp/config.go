package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	StatusConnected    = "CONNECTED"
	StatusReady        = "READY"
	StatusDisconnected = "DISCONNECTED"
)

var (
	ErrInvalidConfig       = errors.New("invalid whatsapp configuration")
	ErrProviderUnavailable = errors.New("whatsapp provider unavailable")
	ErrInstanceNotFound    = errors.New("whatsapp instance not found")
	ErrInstanceNotReady    = errors.New("whatsapp instance is not connected or ready")
	ErrNotConfigured       = errors.New("whatsapp provider is not configured")
	ErrNoActiveInstance    = errors.New("no active whatsapp instance")
)

type Instance struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Phone  string `json:"phone,omitempty"`
	Status string `json:"status"`
}

type ConfigInput struct {
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	Credential string `json:"credential"`
}

type SafeConfig struct {
	Provider             string    `json:"provider"`
	BaseURL              string    `json:"base_url"`
	CredentialConfigured bool      `json:"credential_configured"`
	ActiveInstanceID     string    `json:"active_instance_id,omitempty"`
	ActiveInstancePhone  string    `json:"active_instance_phone,omitempty"`
	ProviderStatus       string    `json:"provider_status,omitempty"`
	SDRStatus            string    `json:"sdr_status"`
	LastVerifiedAt       time.Time `json:"last_verified_at,omitempty"`
}

type WhatsAppProvider interface {
	ValidateConnection(ctx context.Context, credential string) error
	ListInstances(ctx context.Context, credential string) ([]Instance, error)
	GetInstanceStatus(ctx context.Context, credential, instanceID string) (Instance, error)
	SendMessage(ctx context.Context, credential, instanceID, to, body string) error
}

type ProviderRegistry struct {
	providers map[string]WhatsAppProvider
}

func NewRegistry(providers map[string]WhatsAppProvider) *ProviderRegistry {
	copyProviders := make(map[string]WhatsAppProvider, len(providers))
	for name, provider := range providers {
		copyProviders[name] = provider
	}
	return &ProviderRegistry{providers: copyProviders}
}

func (r *ProviderRegistry) Get(name string) (WhatsAppProvider, bool) {
	provider, ok := r.providers[name]
	return provider, ok
}

type RuntimeBinding interface {
	SetActiveWhatsAppInstance(context.Context, Instance) error
}

type Service struct {
	mu       sync.RWMutex
	registry *ProviderRegistry
	binding  RuntimeBinding
	provider string
	baseURL  string
	secret   string
	active   Instance
	verified time.Time
}

func NewService(registry *ProviderRegistry, binding RuntimeBinding) *Service {
	if registry == nil {
		registry = NewRegistry(nil)
	}
	return &Service{registry: registry, binding: binding}
}

func (s *Service) Configure(ctx context.Context, input ConfigInput) (SafeConfig, error) {
	providerName := strings.TrimSpace(input.Provider)
	if providerName == "" {
		return SafeConfig{}, fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	if err := ValidateBaseURL(input.BaseURL); err != nil {
		return SafeConfig{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if strings.TrimSpace(input.Credential) == "" {
		return SafeConfig{}, fmt.Errorf("%w: credential is required", ErrInvalidConfig)
	}
	provider, ok := s.registry.Get(providerName)
	if !ok {
		return SafeConfig{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, providerName)
	}
	if err := provider.ValidateConnection(ctx, input.Credential); err != nil {
		return SafeConfig{}, fmt.Errorf("validate provider connection: %w", err)
	}
	s.mu.Lock()
	s.provider, s.baseURL, s.secret, s.active, s.verified = providerName, strings.TrimSpace(input.BaseURL), input.Credential, Instance{}, time.Now().UTC()
	s.mu.Unlock()
	return s.safeConfig(StatusConnected), nil
}

func (s *Service) Discover(ctx context.Context) ([]Instance, error) {
	provider, credential, err := s.providerAndCredential()
	if err != nil {
		return nil, err
	}
	instances, err := provider.ListInstances(ctx, credential)
	if err != nil {
		return nil, fmt.Errorf("list whatsapp instances: %w", err)
	}
	if len(instances) > 1000 {
		return nil, errors.New("provider returned too many instances")
	}
	for i := range instances {
		instances[i].Status = strings.ToUpper(strings.TrimSpace(instances[i].Status))
	}
	return instances, nil
}

func (s *Service) SelectInstance(ctx context.Context, instanceID string) (SafeConfig, error) {
	if strings.TrimSpace(instanceID) == "" {
		return SafeConfig{}, errors.New("instance_id is required")
	}
	provider, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	instances, err := provider.ListInstances(ctx, credential)
	if err != nil {
		return SafeConfig{}, fmt.Errorf("list whatsapp instances: %w", err)
	}
	var selected Instance
	found := false
	for _, instance := range instances {
		if instance.ID == instanceID {
			selected, found = instance, true
			break
		}
	}
	if !found {
		return SafeConfig{}, ErrInstanceNotFound
	}
	status, err := provider.GetInstanceStatus(ctx, credential, instanceID)
	if err != nil {
		return SafeConfig{}, fmt.Errorf("get whatsapp instance status: %w", err)
	}
	if !ready(status.Status) {
		return SafeConfig{}, ErrInstanceNotReady
	}
	selected.Status, selected.Phone = status.Status, firstNonEmpty(status.Phone, selected.Phone)
	if s.binding != nil {
		if err := s.binding.SetActiveWhatsAppInstance(ctx, selected); err != nil {
			return SafeConfig{}, fmt.Errorf("bind active whatsapp instance: %w", err)
		}
	}
	s.mu.Lock()
	s.active, s.verified = selected, time.Now().UTC()
	s.mu.Unlock()
	return s.safeConfig(selected.Status), nil
}

func (s *Service) Get(ctx context.Context) (SafeConfig, error) {
	provider, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	status := StatusConnected
	if active.ID != "" {
		refreshed, refreshErr := provider.GetInstanceStatus(ctx, credential, active.ID)
		if refreshErr != nil {
			return SafeConfig{}, refreshErr
		}
		active.Status, active.Phone, status = refreshed.Status, firstNonEmpty(refreshed.Phone, active.Phone), refreshed.Status
		s.mu.Lock()
		s.active = active
		s.mu.Unlock()
	}
	return s.safeConfig(status), nil
}

func (s *Service) Test(ctx context.Context) (SafeConfig, error) {
	provider, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	if err := provider.ValidateConnection(ctx, credential); err != nil {
		return SafeConfig{}, fmt.Errorf("validate provider connection: %w", err)
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active.ID == "" {
		return SafeConfig{}, ErrNoActiveInstance
	}
	refreshed, err := provider.GetInstanceStatus(ctx, credential, active.ID)
	if err != nil {
		return SafeConfig{}, ErrInstanceNotFound
	}
	if !ready(refreshed.Status) {
		return SafeConfig{}, ErrInstanceNotReady
	}
	s.mu.Lock()
	s.active, s.verified = refreshed, time.Now().UTC()
	s.mu.Unlock()
	return s.safeConfig(refreshed.Status), nil
}

func (s *Service) providerAndCredential() (WhatsAppProvider, string, error) {
	s.mu.RLock()
	name, credential := s.provider, s.secret
	s.mu.RUnlock()
	if name == "" || credential == "" {
		return nil, "", ErrNotConfigured
	}
	provider, ok := s.registry.Get(name)
	if !ok {
		return nil, "", ErrProviderUnavailable
	}
	return provider, credential, nil
}

func (s *Service) safeConfig(status string) SafeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SafeConfig{Provider: s.provider, BaseURL: s.baseURL, CredentialConfigured: s.secret != "", ActiveInstanceID: s.active.ID, ActiveInstancePhone: s.active.Phone, ProviderStatus: status, SDRStatus: "CONFIGURED", LastVerifiedAt: s.verified}
}

func ValidateBaseURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return errors.New("base_url must be a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("base_url protocol must be http or https")
	}
	if u.User != nil {
		return errors.New("base_url must not contain credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("base_url host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && forbiddenIP(ip) {
		return errors.New("base_url host is not allowed")
	}
	if resolved, lookupErr := net.LookupIP(host); lookupErr == nil {
		for _, ip := range resolved {
			if forbiddenIP(ip) {
				return errors.New("base_url resolves to a private host")
			}
		}
	}
	return nil
}

func forbiddenIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}

func ready(status string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(status))
	return normalized == StatusConnected || normalized == StatusReady
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
