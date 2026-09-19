package sip

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
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
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	// Strict TLS: InsecureSkipVerify is false by default. ServerName set to target hostname.
	tlsConfig := &tls.Config{
		ServerName: host,
	}
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
	CheckAsteriskHealth(ctx context.Context) (bool, error)
	CheckEndpoint(ctx context.Context, trunkName string) (bool, error)
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

// getConfigPath validates trunkName against allowlist and ensures the final clean path stays inside configDir.
func (r *RealAsteriskReloader) getConfigPath(trunkName string) (string, error) {
	if !trunkNameRegex.MatchString(trunkName) {
		return "", fmt.Errorf("invalid trunk name %q: violates security allowlist", trunkName)
	}
	cleanDir := filepath.Clean(r.configDir)
	targetPath := filepath.Clean(filepath.Join(cleanDir, fmt.Sprintf("%s.conf", trunkName)))
	rel, err := filepath.Rel(cleanDir, targetPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("security violation: path traversal detected for trunk %q", trunkName)
	}
	return targetPath, nil
}

// copyFile performs a secure file copy with 0600 permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// ApplyPJSIPConfig executes an atomic PJSIP transaction (temp -> validate -> backup -> rename -> reload).
func (r *RealAsteriskReloader) ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	if err := os.MkdirAll(r.configDir, 0755); err != nil {
		return fmt.Errorf("failed to create Asterisk config dir %s: %w", r.configDir, err)
	}

	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}

	tmpPath := targetPath + ".tmp"
	bakPath := targetPath + ".bak"

	// Step 1: Write temp file with 0600 permissions
	if err := os.WriteFile(tmpPath, []byte(pjsipConf), 0600); err != nil {
		return fmt.Errorf("failed to write temp PJSIP config file %s: %w", tmpPath, err)
	}

	// Step 2: Preserve existing config if present
	hasPrev := false
	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFile(targetPath, bakPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("failed to backup existing PJSIP config file: %w", err)
		}
		hasPrev = true
	}

	// Step 3: Atomic rename
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		if hasPrev {
			_ = os.Remove(bakPath)
		}
		return fmt.Errorf("failed to atomically apply PJSIP config file: %w", err)
	}

	// Step 4: Reload Asterisk PJSIP module
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		// Rollback on reload failure
		if hasPrev {
			_ = os.Rename(bakPath, targetPath)
			_, _ = r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
		} else {
			_ = os.Remove(targetPath)
			_, _ = r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
		}
		return fmt.Errorf("asterisk reload failed during trunk apply: %w, output: %s", err, out)
	}

	if hasPrev {
		_ = os.Remove(bakPath)
	}
	return nil
}

// RemovePJSIPConfig executes an atomic disable transaction with rollback on failure.
func (r *RealAsteriskReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}

	bakPath := targetPath + ".bak"
	hasPrev := false

	if _, err := os.Stat(targetPath); err == nil {
		if err := copyFile(targetPath, bakPath); err != nil {
			return fmt.Errorf("failed to backup config prior to removal: %w", err)
		}
		hasPrev = true
		if err := os.Remove(targetPath); err != nil {
			_ = os.Remove(bakPath)
			return fmt.Errorf("failed to remove PJSIP config file %s: %w", targetPath, err)
		}
	}

	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		// Rollback on reload failure
		if hasPrev {
			_ = os.Rename(bakPath, targetPath)
			_, _ = r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
		}
		return fmt.Errorf("asterisk reload failed during trunk removal: %w, output: %s", err, out)
	}

	if hasPrev {
		_ = os.Remove(bakPath)
	}
	return nil
}

// CheckAsteriskHealth checks if Asterisk service process is responsive.
func (r *RealAsteriskReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "core show status")
	if err != nil {
		return false, fmt.Errorf("asterisk health check failed: %w, output: %s", err, out)
	}
	if strings.Contains(strings.ToLower(out), "asterisk") {
		return true, nil
	}
	return false, fmt.Errorf("asterisk status output invalid: %s", out)
}

// CheckEndpoint queries Asterisk CLI to confirm the endpoint was loaded into Asterisk runtime.
func (r *RealAsteriskReloader) CheckEndpoint(ctx context.Context, trunkName string) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show endpoint trunk-%s", trunkName))
	if err != nil {
		return false, fmt.Errorf("failed to query endpoint status: %w", err)
	}

	lowerOut := strings.ToLower(out)
	if strings.Contains(lowerOut, "unable to find object") || strings.Contains(lowerOut, "no objects found") {
		return false, fmt.Errorf("endpoint trunk-%s not found in Asterisk", trunkName)
	}
	if strings.Contains(lowerOut, "endpoint:") || strings.Contains(lowerOut, "objects found: 1") || strings.Contains(lowerOut, strings.ToLower(trunkName)) {
		return true, nil
	}

	return false, fmt.Errorf("endpoint trunk-%s not found in Asterisk output: %s", trunkName, out)
}

// CheckRegistration queries Asterisk CLI to verify outbound trunk registration status.
func (r *RealAsteriskReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show registration trunk-%s-reg", trunkName))
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

	// Handling Enabled = false (Disable transaction)
	if !cfg.Enabled {
		m.mu.Lock()
		prevCfg, exists := m.trunks[cfg.Name]
		m.mu.Unlock()

		if exists && prevCfg.Enabled {
			// Remove PJSIP config from Asterisk with rollback on failure
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

	// Step 4: Reconcile / Apply PJSIP with Asterisk
	if err := m.reloader.ApplyPJSIPConfig(ctx, cfg.Name, pjsipConf); err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("Asterisk reload error: %v", err)
		return report, fmt.Errorf("asterisk reload failed: %w", err)
	}

	// Step 5: Check Asterisk Health
	healthy, healthErr := m.reloader.CheckAsteriskHealth(ctx)
	report.AsteriskHealthy = healthy
	if healthErr != nil || !healthy {
		_ = m.reloader.RemovePJSIPConfig(ctx, cfg.Name) // Rollback applied config
		report.Status = StatusConnectionError
		report.EndpointActive = false
		report.LastError = fmt.Sprintf("Asterisk health check failed: %v", healthErr)
		return report, fmt.Errorf("asterisk health check failed: %w", healthErr)
	}

	// Step 6: Check Endpoint Existence in Asterisk
	epActive, epErr := m.reloader.CheckEndpoint(ctx, cfg.Name)
	report.EndpointActive = epActive
	if epErr != nil || !epActive {
		_ = m.reloader.RemovePJSIPConfig(ctx, cfg.Name) // Rollback applied config
		report.Status = StatusConfigured
		report.EndpointActive = false
		report.LastError = fmt.Sprintf("Endpoint check failed: %v", epErr)
		return report, fmt.Errorf("asterisk endpoint check failed: %w", epErr)
	}

	// Step 7: Verify Registration if required
	if cfg.RegistrationRequired {
		regState, regHealthy, regErr := m.reloader.CheckRegistration(ctx, cfg.Name)
		report.RegistrationState = regState
		if regErr != nil || !regHealthy || (regState != "Registered" && regState != "REGISTERED") {
			_ = m.reloader.RemovePJSIPConfig(ctx, cfg.Name) // Rollback applied config
			report.Status = StatusRegistrationFailed
			report.EndpointActive = false
			report.LastError = fmt.Sprintf("Registration failed: state=%s, err=%v", regState, regErr)
			return report, fmt.Errorf("registration failed: state=%s, err=%v", regState, regErr)
		}
		report.Status = StatusReady
		report.EndpointActive = true
	} else {
		report.RegistrationState = "N/A"
		report.Status = StatusReady
		report.EndpointActive = true
	}

	// Commit state safely only after full success
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
