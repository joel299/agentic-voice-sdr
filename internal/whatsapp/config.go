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
	ErrProviderOperation   = errors.New("whatsapp provider operation failed")
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
	ValidateConnection(ctx context.Context, baseURL, credential string) error
	ListInstances(ctx context.Context, baseURL, credential string) ([]Instance, error)
	GetInstanceStatus(ctx context.Context, baseURL, credential, instanceID string) (Instance, error)
	SendMessage(ctx context.Context, baseURL, credential, instanceID, to, body string) error
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
	ClearActiveWhatsAppInstance(context.Context) error
}

type Service struct {
	mu           sync.RWMutex
	transitionMu sync.Mutex
	registry     *ProviderRegistry
	binding      RuntimeBinding
	store        ConfigStore
	provider     string
	baseURL      string
	secret       string
	active       Instance
	verified     time.Time
}

func NewService(registry *ProviderRegistry, binding RuntimeBinding) *Service {
	return NewServiceWithStore(registry, binding, NewMemoryConfigStore())
}

func NewServiceWithStore(registry *ProviderRegistry, binding RuntimeBinding, store ConfigStore) *Service {
	if registry == nil {
		registry = NewRegistry(nil)
	}
	service := &Service{registry: registry, binding: binding, store: store}
	if store != nil {
		if metadata, err := store.Load(context.Background()); err == nil {
			service.provider = metadata.Provider
			service.baseURL = metadata.BaseURL
			service.active = Instance{ID: metadata.ActiveInstanceID, Phone: metadata.ActiveInstancePhone, Status: metadata.ProviderStatus}
			service.verified = metadata.LastVerifiedAt
		}
	}
	return service
}

func (s *Service) Configure(ctx context.Context, input ConfigInput) (SafeConfig, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
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
	if err := provider.ValidateConnection(ctx, input.BaseURL, input.Credential); err != nil {
		return SafeConfig{}, fmt.Errorf("%w: validate provider connection: %v", ErrProviderOperation, err)
	}
	if s.binding != nil {
		if err := s.binding.ClearActiveWhatsAppInstance(ctx); err != nil {
			return SafeConfig{}, fmt.Errorf("%w: clear runtime binding: %v", ErrProviderOperation, err)
		}
	}
	s.mu.Lock()
	s.provider, s.baseURL, s.secret, s.active, s.verified = providerName, strings.TrimSpace(input.BaseURL), input.Credential, Instance{}, time.Now().UTC()
	s.mu.Unlock()
	if err := s.saveMetadata(ctx); err != nil {
		return SafeConfig{}, fmt.Errorf("%w: persist metadata: %v", ErrProviderOperation, err)
	}
	return s.safeConfig(StatusConnected), nil
}

func (s *Service) Discover(ctx context.Context) ([]Instance, error) {
	provider, baseURL, credential, err := s.providerAndCredential()
	if err != nil {
		return nil, err
	}
	instances, err := provider.ListInstances(ctx, baseURL, credential)
	if err != nil {
		return nil, fmt.Errorf("%w: list instances: %v", ErrProviderOperation, err)
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
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if strings.TrimSpace(instanceID) == "" {
		return SafeConfig{}, errors.New("instance_id is required")
	}
	provider, baseURL, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	instances, err := provider.ListInstances(ctx, baseURL, credential)
	if err != nil {
		return SafeConfig{}, fmt.Errorf("%w: list instances: %v", ErrProviderOperation, err)
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
	status, err := provider.GetInstanceStatus(ctx, baseURL, credential, instanceID)
	if err != nil {
		if errors.Is(err, ErrInstanceNotFound) {
			return SafeConfig{}, ErrInstanceNotFound
		}
		return SafeConfig{}, fmt.Errorf("%w: get instance status: %v", ErrProviderOperation, err)
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
	if err := s.saveMetadata(ctx); err != nil {
		return SafeConfig{}, fmt.Errorf("%w: persist metadata: %v", ErrProviderOperation, err)
	}
	return s.safeConfig(selected.Status), nil
}

func (s *Service) Get(ctx context.Context) (SafeConfig, error) {
	provider, baseURL, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	status := StatusConnected
	if active.ID != "" {
		refreshed, refreshErr := provider.GetInstanceStatus(ctx, baseURL, credential, active.ID)
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrInstanceNotFound) {
				return SafeConfig{}, ErrInstanceNotFound
			}
			return SafeConfig{}, fmt.Errorf("%w: refresh instance status: %v", ErrProviderOperation, refreshErr)
		}
		active.ID = firstNonEmpty(refreshed.ID, active.ID)
		active.Name = firstNonEmpty(refreshed.Name, active.Name)
		active.Status = firstNonEmpty(refreshed.Status, active.Status)
		active.Phone = firstNonEmpty(refreshed.Phone, active.Phone)
		status = active.Status
		s.mu.Lock()
		s.active = active
		s.mu.Unlock()
	}
	return s.safeConfig(status), nil
}

func (s *Service) Test(ctx context.Context) (SafeConfig, error) {
	provider, baseURL, credential, err := s.providerAndCredential()
	if err != nil {
		return SafeConfig{}, err
	}
	if err := provider.ValidateConnection(ctx, baseURL, credential); err != nil {
		return SafeConfig{}, fmt.Errorf("%w: validate provider connection: %v", ErrProviderOperation, err)
	}
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	if active.ID == "" {
		return SafeConfig{}, ErrNoActiveInstance
	}
	refreshed, err := provider.GetInstanceStatus(ctx, baseURL, credential, active.ID)
	if err != nil {
		if errors.Is(err, ErrInstanceNotFound) {
			return SafeConfig{}, ErrInstanceNotFound
		}
		return SafeConfig{}, fmt.Errorf("%w: refresh instance status: %v", ErrProviderOperation, err)
	}
	refreshed.ID = firstNonEmpty(refreshed.ID, active.ID)
	refreshed.Name = firstNonEmpty(refreshed.Name, active.Name)
	refreshed.Phone = firstNonEmpty(refreshed.Phone, active.Phone)
	if !ready(refreshed.Status) {
		return SafeConfig{}, ErrInstanceNotReady
	}
	s.mu.Lock()
	s.active, s.verified = refreshed, time.Now().UTC()
	s.mu.Unlock()
	return s.safeConfig(refreshed.Status), nil
}

func (s *Service) providerAndCredential() (WhatsAppProvider, string, string, error) {
	s.mu.RLock()
	name, baseURL, credential := s.provider, s.baseURL, s.secret
	s.mu.RUnlock()
	if name == "" || credential == "" {
		return nil, "", "", ErrNotConfigured
	}
	provider, ok := s.registry.Get(name)
	if !ok {
		return nil, "", "", ErrProviderUnavailable
	}
	return provider, baseURL, credential, nil
}

func (s *Service) safeConfig(status string) SafeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SafeConfig{Provider: s.provider, BaseURL: s.baseURL, CredentialConfigured: s.secret != "", ActiveInstanceID: s.active.ID, ActiveInstancePhone: s.active.Phone, ProviderStatus: status, SDRStatus: "CONFIGURED", LastVerifiedAt: s.verified}
}

func (s *Service) saveMetadata(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	s.mu.RLock()
	metadata := ConfigMetadata{Provider: s.provider, BaseURL: s.baseURL, ActiveInstanceID: s.active.ID, ActiveInstancePhone: s.active.Phone, ProviderStatus: s.active.Status, LastVerifiedAt: s.verified}
	s.mu.RUnlock()
	return s.store.Save(ctx, metadata)
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
