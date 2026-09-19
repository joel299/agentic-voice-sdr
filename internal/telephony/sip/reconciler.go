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

// AsteriskReloader abstracts Asterisk PJSIP config staging, committing, rollback, removal, and status checks.
type AsteriskReloader interface {
	StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error
	CommitPJSIPConfig(ctx context.Context, trunkName string) error
	RollbackPJSIPConfig(ctx context.Context, trunkName string) error
	ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error
	RemovePJSIPConfig(ctx context.Context, trunkName string) error
	CheckAsteriskHealth(ctx context.Context) (bool, error)
	CheckTransport(ctx context.Context, transport TransportType) (bool, error)
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

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// StagePJSIPConfig stages a new PJSIP config file, preserves any existing config in .bak, and reloads Asterisk.
func (r *RealAsteriskReloader) StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
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
	if err := os.WriteFile(tmpPath, []byte(pjsipConf), 0644); err != nil {
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
		// Rollback on reload failure during stage
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || reloadErr != nil {
				return fmt.Errorf("asterisk reload failed during trunk stage (%w, output: %s); rollback failed (restoreErr: %v, reloadErr: %v)", err, out, rbErr, reloadErr)
			}
		} else {
			removeErr := os.Remove(targetPath)
			_, rollbackReloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if removeErr != nil || rollbackReloadErr != nil {
				return fmt.Errorf("asterisk reload failed during trunk stage (%w, output: %s); rollback failed (removeErr: %v, reloadErr: %v)", err, out, removeErr, rollbackReloadErr)
			}
		}
		return fmt.Errorf("asterisk reload failed during trunk stage: %w, output: %s", err, out)
	}

	// Note: bakPath is kept until CommitPJSIPConfig or RollbackPJSIPConfig!
	return nil
}

// CommitPJSIPConfig finalizes a staged transaction by removing the backup file.
func (r *RealAsteriskReloader) CommitPJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}
	bakPath := targetPath + ".bak"
	if _, err := os.Stat(bakPath); err == nil {
		if err := os.Remove(bakPath); err != nil {
			return fmt.Errorf("failed to remove backup config file during commit: %w", err)
		}
	}
	return nil
}

// RollbackPJSIPConfig reverts a staged transaction, restoring any previous config from .bak or removing targetPath, and reloads Asterisk.
func (r *RealAsteriskReloader) RollbackPJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}
	bakPath := targetPath + ".bak"

	var restoreErr error
	if _, err := os.Stat(bakPath); err == nil {
		// Restore previous config
		restoreErr = os.Rename(bakPath, targetPath)
	} else {
		// No previous config: remove targetPath
		if _, err := os.Stat(targetPath); err == nil {
			restoreErr = os.Remove(targetPath)
		}
	}

	// Reload Asterisk to apply restored or removed state
	out, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")

	if restoreErr != nil || reloadErr != nil {
		return fmt.Errorf("rollback failed: restoreErr=%v, reloadErr=%v, output=%s", restoreErr, reloadErr, out)
	}
	return nil
}

// ApplyPJSIPConfig performs StagePJSIPConfig followed by CommitPJSIPConfig.
func (r *RealAsteriskReloader) ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	if err := r.StagePJSIPConfig(ctx, trunkName, pjsipConf); err != nil {
		return err
	}
	return r.CommitPJSIPConfig(ctx, trunkName)
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
		// Rollback on reload failure during removal
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || reloadErr != nil {
				return fmt.Errorf("asterisk reload failed during trunk removal (%w, output: %s); rollback failed (restoreErr: %v, reloadErr: %v)", err, out, rbErr, reloadErr)
			}
		}
		return fmt.Errorf("asterisk reload failed during trunk removal: %w, output: %s", err, out)
	}

	// Verify health post-removal
	healthy, healthErr := r.CheckAsteriskHealth(ctx)
	if healthErr != nil || !healthy {
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || reloadErr != nil {
				return fmt.Errorf("asterisk unhealthy after trunk removal (%v); rollback failed (restoreErr: %v, reloadErr: %v)", healthErr, rbErr, reloadErr)
			}
		}
		return fmt.Errorf("asterisk unhealthy after trunk removal: %w", healthErr)
	}

	if hasPrev {
		_ = os.Remove(bakPath)
	}
	return nil
}

// CheckTransport queries Asterisk CLI to confirm the specified shared transport is provisioned in Asterisk.
func (r *RealAsteriskReloader) CheckTransport(ctx context.Context, transport TransportType) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show transport transport-%s", transport))
	if err != nil {
		return false, fmt.Errorf("asterisk CLI query failed for transport transport-%s: %w", transport, err)
	}

	lowerOut := strings.ToLower(out)
	if strings.Contains(lowerOut, "unable to find object") || strings.Contains(lowerOut, "no objects found") || strings.Contains(lowerOut, "not found") {
		return false, fmt.Errorf("transport transport-%s not configured in Asterisk", transport)
	}
	if strings.Contains(lowerOut, "transport:") || strings.Contains(lowerOut, "objects found: 1") || strings.Contains(lowerOut, fmt.Sprintf("transport-%s", transport)) {
		return true, nil
	}

	return false, fmt.Errorf("transport transport-%s not found in Asterisk output: %s", transport, out)
}

// CheckAsteriskHealth checks if Asterisk service process is responsive.
func (r *RealAsteriskReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "core show version")
	if err != nil {
		out, err = r.runner.RunCommand(ctx, "asterisk", "-rx", "core show uptime")
	}
	if err != nil {
		return false, fmt.Errorf("asterisk health check failed: %w, output: %s", err, out)
	}
	if strings.Contains(strings.ToLower(out), "asterisk") || strings.Contains(strings.ToLower(out), "system uptime") {
		return true, nil
	}
	return false, fmt.Errorf("asterisk status output invalid: %s", out)
}

// CheckEndpoint queries Asterisk CLI to confirm the endpoint was loaded into Asterisk runtime.
func (r *RealAsteriskReloader) CheckEndpoint(ctx context.Context, trunkName string) (bool, error) {
	for attempt := 0; attempt < 10; attempt++ {
		out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show endpoint trunk-%s", trunkName))
		if err == nil {
			lowerOut := strings.ToLower(out)
			if (strings.Contains(lowerOut, "endpoint:") || strings.Contains(lowerOut, strings.ToLower(trunkName))) && !strings.Contains(lowerOut, "unable to find object") {
				return true, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

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
	mu             sync.RWMutex
	globalReloadMu sync.Mutex
	trunkLocks     map[string]*sync.Mutex
	trunks         map[string]TrunkConfig
	statuses       map[string]StatusReport
	pjsipConfigs   map[string]string
	dialer         NetworkDialer
	reloader       AsteriskReloader
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
		trunkLocks:   make(map[string]*sync.Mutex),
		trunks:       make(map[string]TrunkConfig),
		statuses:     make(map[string]StatusReport),
		pjsipConfigs: make(map[string]string),
		dialer:       dialer,
		reloader:     reloader,
	}, nil
}

// getTrunkLock returns a dedicated mutex for the specified trunk name, allocating one if needed.
func (m *Manager) getTrunkLock(trunkName string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.trunkLocks[trunkName]
	if !ok {
		lock = &sync.Mutex{}
		m.trunkLocks[trunkName] = lock
	}
	return lock
}

// ApplyTrunk validates, reconciles, and applies a SIP trunk configuration safely inside an atomic transaction.
func (m *Manager) ApplyTrunk(ctx context.Context, cfg TrunkConfig) (StatusReport, error) {
	// Deep clone input config to prevent caller slice mutation
	cfg = cfg.Clone()

	// Acquire per-trunk mutex to serialize all transactional operations for this trunk name
	trunkLock := m.getTrunkLock(cfg.Name)
	trunkLock.Lock()
	defer trunkLock.Unlock()

	// Acquire global reload mutex to serialize Asterisk runtime reload/verification transactions
	m.globalReloadMu.Lock()
	defer m.globalReloadMu.Unlock()

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

	// Step 4: Stage PJSIP config with Asterisk
	if err := m.reloader.StagePJSIPConfig(ctx, cfg.Name, pjsipConf); err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("Asterisk reload error: %v", err)
		return report, fmt.Errorf("asterisk reload failed: %w", err)
	}

	// Helper function for transaction rollback on verification failure
	rollbackTransaction := func(origErr error, status LifecycleStatus) (StatusReport, error) {
		rbErr := m.reloader.RollbackPJSIPConfig(ctx, cfg.Name)
		report.Status = status
		report.EndpointActive = false
		if rbErr != nil {
			report.LastError = fmt.Sprintf("%v; rollback error: %v", origErr, rbErr)
			return report, fmt.Errorf("operation failed (%w) and rollback failed: %v", origErr, rbErr)
		}
		report.LastError = origErr.Error()
		return report, origErr
	}

	// Step 5: Check Asterisk Health
	healthy, healthErr := m.reloader.CheckAsteriskHealth(ctx)
	report.AsteriskHealthy = healthy
	if healthErr != nil || !healthy {
		if healthErr == nil {
			healthErr = fmt.Errorf("asterisk health status is unhealthy")
		}
		return rollbackTransaction(fmt.Errorf("asterisk health check failed: %w", healthErr), StatusConnectionError)
	}

	// Step 5b: Check Shared Transport Provisioned in Asterisk
	tpActive, tpErr := m.reloader.CheckTransport(ctx, cfg.Transport)
	if tpErr != nil || !tpActive {
		if tpErr == nil {
			tpErr = fmt.Errorf("shared transport transport-%s not provisioned in Asterisk", cfg.Transport)
		}
		return rollbackTransaction(fmt.Errorf("asterisk transport check failed: %w", tpErr), StatusConfigured)
	}

	// Step 6: Check Endpoint Existence in Asterisk
	epActive, epErr := m.reloader.CheckEndpoint(ctx, cfg.Name)
	report.EndpointActive = epActive
	if epErr != nil || !epActive {
		if epErr == nil {
			epErr = fmt.Errorf("endpoint trunk-%s not active in Asterisk", cfg.Name)
		}
		return rollbackTransaction(fmt.Errorf("asterisk endpoint check failed: %w", epErr), StatusConfigured)
	}

	// Step 7: Verify Registration if required
	if cfg.RegistrationRequired {
		regState, regHealthy, regErr := m.reloader.CheckRegistration(ctx, cfg.Name)
		report.RegistrationState = regState
		if regErr != nil || !regHealthy || (regState != "Registered" && regState != "REGISTERED") {
			if regErr == nil {
				regErr = fmt.Errorf("registration state %q is not Registered", regState)
			}
			return rollbackTransaction(fmt.Errorf("registration failed: %w", regErr), StatusRegistrationFailed)
		}
		report.Status = StatusReady
		report.EndpointActive = true
	} else {
		report.RegistrationState = "N/A"
		report.Status = StatusReady
		report.EndpointActive = true
	}

	// Step 8: Commit PJSIP config transaction on full success
	if err := m.reloader.CommitPJSIPConfig(ctx, cfg.Name); err != nil {
		return rollbackTransaction(fmt.Errorf("failed to commit PJSIP config: %w", err), StatusConfigured)
	}

	// Commit state safely in Manager
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
