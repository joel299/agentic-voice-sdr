package sip

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NetworkDialer abstracts network dialing for deterministic testing.
type NetworkDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	DialTLSContext(ctx context.Context, network, address string) (net.Conn, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DefaultNetworkDialer implements NetworkDialer using standard net and crypto/tls packages.
type DefaultNetworkDialer struct{}

func (d DefaultNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

func (d DefaultNetworkDialer) DialTLSContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	conn, err := tls.DialWithDialer(dialer, network, address, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("tls handshake failed for %s: %w", address, err)
	}
	return conn, nil
}

func (d DefaultNetworkDialer) LookupHost(ctx context.Context, host string) ([]string, error) {
	resolver := net.DefaultResolver
	return resolver.LookupHost(ctx, host)
}

// CommandRunner abstracts CLI execution for testing.
type CommandRunner interface {
	RunCommand(ctx context.Context, name string, args ...string) (string, error)
}

// OSCommandRunner implements CommandRunner using os/exec.
type OSCommandRunner struct{}

func (r OSCommandRunner) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// AsteriskReloader abstracts Asterisk PJSIP config reloading, application, removal, and status checks.
type AsteriskReloader interface {
	ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error
	RemovePJSIPConfig(ctx context.Context, trunkName string) error
	CheckRegistration(ctx context.Context, trunkName string) (string, bool, error)
}

// RealAsteriskReloader is the operational concrete implementation for Asterisk PJSIP integration.
type RealAsteriskReloader struct {
	configDir string
	runner    CommandRunner
}

// NewRealAsteriskReloader creates an operational RealAsteriskReloader instance.
func NewRealAsteriskReloader(configDir string, runner CommandRunner) *RealAsteriskReloader {
	if configDir == "" {
		configDir = "/etc/asterisk/pjsip.d"
	}
	if runner == nil {
		runner = OSCommandRunner{}
	}
	return &RealAsteriskReloader{
		configDir: configDir,
		runner:    runner,
	}
}

func (r *RealAsteriskReloader) ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	if err := os.MkdirAll(r.configDir, 0755); err != nil {
		return fmt.Errorf("failed to create Asterisk config dir %s: %w", r.configDir, err)
	}

	filename := filepath.Join(r.configDir, fmt.Sprintf("%s.conf", trunkName))

	// Write file securely with 0600 permissions to prevent secret leakage
	if err := os.WriteFile(filename, []byte(pjsipConf), 0600); err != nil {
		return fmt.Errorf("failed to write PJSIP config file %s: %w", filename, err)
	}

	// Execute Asterisk reload
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		return fmt.Errorf("asterisk reload failed: %w, output: %s", err, out)
	}
	return nil
}

func (r *RealAsteriskReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	filename := filepath.Join(r.configDir, fmt.Sprintf("%s.conf", trunkName))

	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove PJSIP config file %s: %w", filename, err)
	}

	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		return fmt.Errorf("asterisk reload failed during trunk removal: %w, output: %s", err, out)
	}
	return nil
}

func (r *RealAsteriskReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show registration %s-reg", trunkName))
	if err != nil {
		return "Unregistered", false, fmt.Errorf("failed to query registration status: %w", err)
	}

	lowerOut := strings.ToLower(out)
	if strings.Contains(lowerOut, "registered") && !strings.Contains(lowerOut, "unregistered") {
		return "Registered", true, nil
	} else if strings.Contains(lowerOut, "rejected") {
		return "Rejected", false, fmt.Errorf("registration rejected by remote server")
	} else if strings.Contains(lowerOut, "auth_failed") || strings.Contains(lowerOut, "authentication failed") {
		return "Authentication Failed", false, fmt.Errorf("authentication failed")
	}

	return "Unregistered", false, fmt.Errorf("trunk registration state is not Registered: %s", out)
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

// NewManager creates a new SIP Manager instance. Returns error if required dependencies are nil.
func NewManager(dialer NetworkDialer, reloader AsteriskReloader) (*Manager, error) {
	if dialer == nil {
		return nil, fmt.Errorf("dialer is required and cannot be nil")
	}
	if reloader == nil {
		return nil, fmt.Errorf("reloader is required and cannot be nil; mock reloader is for test use only")
	}
	return &Manager{
		trunks:       make(map[string]TrunkConfig),
		statuses:     make(map[string]StatusReport),
		pjsipConfigs: make(map[string]string),
		dialer:       dialer,
		reloader:     reloader,
	}, nil
}

// ApplyTrunk validates, reconciles, and applies a SIP trunk configuration safely.
func (m *Manager) ApplyTrunk(ctx context.Context, cfg TrunkConfig) (StatusReport, error) {
	// Deep clone input config to prevent caller slice mutation
	cfg = cfg.Clone()

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

	// Handling Enabled = false
	if !cfg.Enabled {
		m.mu.Lock()
		prevCfg, exists := m.trunks[cfg.Name]
		m.mu.Unlock()

		if exists && prevCfg.Enabled {
			// Remove PJSIP config from Asterisk
			if err := m.reloader.RemovePJSIPConfig(ctx, cfg.Name); err != nil {
				return StatusReport{}, fmt.Errorf("failed to disable trunk in Asterisk: %w", err)
			}
		}

		report := StatusReport{
			TrunkName:         cfg.Name,
			Provider:          cfg.Provider,
			Status:            StatusDisabled,
			EndpointActive:    false,
			RegistrationState: "Disabled",
			LastValidatedAt:   time.Now(),
		}

		m.mu.Lock()
		m.trunks[cfg.Name] = cfg
		m.statuses[cfg.Name] = report
		delete(m.pjsipConfigs, cfg.Name)
		m.mu.Unlock()

		return report, nil
	}

	report := StatusReport{
		TrunkName:       cfg.Name,
		Provider:        cfg.Provider,
		Status:          StatusValidating,
		LastValidatedAt: time.Now(),
	}

	// Step 1: DNS Resolution Check
	_, err := m.dialer.LookupHost(ctx, cfg.Host)
	if err != nil {
		report.Status = StatusDNSError
		report.LastError = fmt.Sprintf("DNS lookup failed for host %s: %v", cfg.Host, err)
		return report, fmt.Errorf("DNS failure: %w", err)
	}

	// Step 2: Connection / Reachability & TLS Check
	address := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	network := string(cfg.Transport)

	if cfg.Transport == TransportTLS {
		conn, err := m.dialer.DialTLSContext(ctx, "tcp", address)
		if err != nil {
			report.Status = StatusConnectionError
			report.LastError = fmt.Sprintf("TLS connection/handshake failed to %s: %v", address, err)
			return report, fmt.Errorf("TLS connection failure: %w", err)
		}
		_ = conn.Close()
	} else {
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
	if err := m.reloader.ApplyPJSIPConfig(ctx, cfg.Name, pjsipConf); err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("Asterisk reload error: %v", err)
		return report, fmt.Errorf("asterisk reload failed: %w", err)
	}

	// Step 5: Verify Registration & Readiness
	regState, healthy, regErr := m.reloader.CheckRegistration(ctx, cfg.Name)
	report.AsteriskHealthy = healthy
	report.RegistrationState = regState

	if cfg.RegistrationRequired {
		if regErr != nil || !healthy || (regState != "Registered" && regState != "REGISTERED") {
			report.Status = StatusRegistrationFailed
			report.EndpointActive = false
			report.LastError = fmt.Sprintf("Registration failed: state=%s, err=%v", regState, regErr)
			return report, fmt.Errorf("registration failed: state=%s, err=%v", regState, regErr)
		}
		report.Status = StatusReady
		report.EndpointActive = true
	} else {
		if healthy {
			report.Status = StatusReady
			report.EndpointActive = true
		} else {
			report.Status = StatusConfigured
			report.EndpointActive = false
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

// GetTrunk retrieves a cloned applied trunk configuration by name.
func (m *Manager) GetTrunk(name string) (TrunkConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.trunks[name]
	if !ok {
		return TrunkConfig{}, false
	}
	return cfg.Clone(), true
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

// ListTrunks returns all configured trunks with defensive copies.
func (m *Manager) ListTrunks() []TrunkConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]TrunkConfig, 0, len(m.trunks))
	for _, cfg := range m.trunks {
		list = append(list, cfg.Clone())
	}
	return list
}
