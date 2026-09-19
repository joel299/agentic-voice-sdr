package sip

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// NetworkDialer abstracts network dialing for deterministic testing.
type NetworkDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DefaultNetworkDialer implements NetworkDialer using standard net package.
type DefaultNetworkDialer struct{}

func (d DefaultNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

func (d DefaultNetworkDialer) LookupHost(ctx context.Context, host string) ([]string, error) {
	resolver := net.DefaultResolver
	return resolver.LookupHost(ctx, host)
}

// AsteriskReloader abstracts Asterisk PJSIP config reloading and status checks.
type AsteriskReloader interface {
	ReloadPJSIP(ctx context.Context, pjsipConf string) error
	CheckRegistration(ctx context.Context, trunkName string) (string, bool, error)
}

// MockAsteriskReloader provides in-memory simulation for Asterisk operations.
type MockAsteriskReloader struct {
	Healthy           bool
	RegistrationState string
	ReloadErr         error
}

func (m *MockAsteriskReloader) ReloadPJSIP(ctx context.Context, pjsipConf string) error {
	return m.ReloadErr
}

func (m *MockAsteriskReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	state := m.RegistrationState
	if state == "" {
		state = "Registered"
	}
	return state, m.Healthy, nil
}

// Manager coordinates SIP trunk configuration, validation, reconciliation, and status tracking.
type Manager struct {
	mu           sync.RWMutex
	trunks       map[string]TrunkConfig
	statuses     map[string]StatusReport
	pjsipConfigs map[string]string
	dialer       NetworkDialer
	reloader     AsteriskReloader
}

// NewManager creates a new SIP Manager instance.
func NewManager(dialer NetworkDialer, reloader AsteriskReloader) *Manager {
	if dialer == nil {
		dialer = DefaultNetworkDialer{}
	}
	if reloader == nil {
		reloader = &MockAsteriskReloader{Healthy: true, RegistrationState: "Registered"}
	}
	return &Manager{
		trunks:       make(map[string]TrunkConfig),
		statuses:     make(map[string]StatusReport),
		pjsipConfigs: make(map[string]string),
		dialer:       dialer,
		reloader:     reloader,
	}
}

// ApplyTrunk validates, reconciles, and applies a SIP trunk configuration safely.
func (m *Manager) ApplyTrunk(ctx context.Context, cfg TrunkConfig) (StatusReport, error) {
	if err := cfg.Validate(); err != nil {
		report := StatusReport{
			TrunkName:       cfg.Name,
			Provider:        cfg.Provider,
			Status:          StatusAuthError,
			LastError:       fmt.Sprintf("validation failed: %v", err),
			LastValidatedAt: time.Now(),
		}
		return report, fmt.Errorf("invalid trunk configuration: %w", err)
	}

	report := StatusReport{
		TrunkName:       cfg.Name,
		Provider:        cfg.Provider,
		Status:          StatusValidating,
		LastValidatedAt: time.Now(),
	}

	if !cfg.Enabled {
		report.Status = StatusDisabled
		m.mu.Lock()
		m.trunks[cfg.Name] = cfg
		m.statuses[cfg.Name] = report
		m.mu.Unlock()
		return report, nil
	}

	// Step 1: DNS Resolution Check
	_, err := m.dialer.LookupHost(ctx, cfg.Host)
	if err != nil {
		report.Status = StatusDNSError
		report.LastError = fmt.Sprintf("DNS lookup failed for host %s: %v", cfg.Host, err)
		return report, fmt.Errorf("DNS failure: %w", err)
	}

	// Step 2: Connection / Reachability Check
	address := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	network := string(cfg.Transport)
	if cfg.Transport == TransportUDP || cfg.Transport == TransportTCP {
		conn, err := m.dialer.DialContext(ctx, network, address)
		if err != nil {
			report.Status = StatusConnectionError
			report.LastError = fmt.Sprintf("Connection failed to %s via %s: %v", address, network, err)
			return report, fmt.Errorf("connection failure: %w", err)
		}
		_ = conn.Close()
	}

	// Step 3: PJSIP Config Generation
	pjsipConf, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("PJSIP generation error: %v", err)
		return report, fmt.Errorf("PJSIP generation failed: %w", err)
	}

	// Step 4: Reconcile with Asterisk
	if err := m.reloader.ReloadPJSIP(ctx, pjsipConf); err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("Asterisk reload error: %v", err)
		return report, fmt.Errorf("asterisk reload failed: %w", err)
	}

	// Step 5: Verify Registration & Readiness
	regState, healthy, err := m.reloader.CheckRegistration(ctx, cfg.Name)
	report.AsteriskHealthy = healthy
	report.RegistrationState = regState

	if cfg.RegistrationRequired {
		if healthy && (regState == "Registered" || regState == "REGISTERED") {
			report.Status = StatusReady
			report.EndpointActive = true
		} else {
			report.Status = StatusRegistered
			report.EndpointActive = true
		}
	} else {
		if healthy {
			report.Status = StatusReady
			report.EndpointActive = true
		} else {
			report.Status = StatusConfigured
		}
	}

	// Commit state safely only after successful reconciliation
	m.mu.Lock()
	m.trunks[cfg.Name] = cfg
	m.statuses[cfg.Name] = report
	m.pjsipConfigs[cfg.Name] = pjsipConf
	m.mu.Unlock()

	return report, nil
}

// GetTrunk retrieves an applied trunk configuration by name.
func (m *Manager) GetTrunk(name string) (TrunkConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.trunks[name]
	return cfg, ok
}

// GetStatus retrieves the live status report of a trunk.
func (m *Manager) GetStatus(name string) (StatusReport, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status, ok := m.statuses[name]
	return status, ok
}

// GetPJSIPConfig retrieves the generated PJSIP configuration for a trunk.
func (m *Manager) GetPJSIPConfig(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conf, ok := m.pjsipConfigs[name]
	return conf, ok
}

// ListTrunks returns all configured trunks.
func (m *Manager) ListTrunks() []TrunkConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]TrunkConfig, 0, len(m.trunks))
	for _, cfg := range m.trunks {
		list = append(list, cfg)
	}
	return list
}
