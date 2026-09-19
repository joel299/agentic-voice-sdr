package sip_test

import (
	"context"
	"time"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

type mockDialer struct {
	lookupErr error
	dialErr   error
	tlsErr    error
}

func (m *mockDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if m.dialErr != nil {
		return nil, m.dialErr
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (m *mockDialer) DialTLSContext(ctx context.Context, network, address string) (net.Conn, error) {
	if m.tlsErr != nil {
		return nil, m.tlsErr
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (m *mockDialer) LookupHost(ctx context.Context, host string) ([]string, error) {
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	return []string{"192.168.1.100"}, nil
}

type MockAsteriskReloader struct {
	Healthy           bool
	TransportInactive bool
	TransportErr      error
	EndpointActive    bool
	RegistrationState string
	ReloadErr         error
	HealthErr         error
	EndpointErr       error
	StageErr          error
	CommitErr         error
	RollbackErr       error
	RemoveErr         error
	RemoveCalled      bool
	ApplyCalled       bool
	StageCalled       bool
	CommitCalled      bool
	RollbackCalled    bool
}

func (m *MockAsteriskReloader) StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	m.StageCalled = true
	return m.StageErr
}

func (m *MockAsteriskReloader) CommitPJSIPConfig(ctx context.Context, trunkName string) error {
	m.CommitCalled = true
	return m.CommitErr
}

func (m *MockAsteriskReloader) RollbackPJSIPConfig(ctx context.Context, trunkName string) error {
	m.RollbackCalled = true
	return m.RollbackErr
}

func (m *MockAsteriskReloader) ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	m.ApplyCalled = true
	if err := m.StagePJSIPConfig(ctx, trunkName, pjsipConf); err != nil {
		return err
	}
	return m.CommitPJSIPConfig(ctx, trunkName)
}

func (m *MockAsteriskReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	m.RemoveCalled = true
	if m.RemoveErr != nil {
		return m.RemoveErr
	}
	return m.ReloadErr
}

func (m *MockAsteriskReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	if m.HealthErr != nil {
		return false, m.HealthErr
	}
	return m.Healthy, nil
}

func (m *MockAsteriskReloader) CheckTransport(ctx context.Context, transport sip.TransportType) (bool, error) {
	if m.TransportErr != nil {
		return false, m.TransportErr
	}
	if m.TransportInactive {
		return false, fmt.Errorf("transport transport-%s not provisioned in Asterisk", transport)
	}
	return true, nil
}

func (m *MockAsteriskReloader) CheckEndpoint(ctx context.Context, trunkName string) (bool, error) {
	if m.EndpointErr != nil {
		return false, m.EndpointErr
	}
	if !m.EndpointActive && m.EndpointErr == nil {
		return false, errors.New("endpoint not found")
	}
	return true, nil
}

func (m *MockAsteriskReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	state := m.RegistrationState
	if state == "" {
		state = "Registered"
	}
	if state == "Rejected" {
		return "Rejected", false, errors.New("registration rejected")
	}
	if state == "Failed" {
		return "Failed", false, errors.New("registration failed")
	}
	return state, m.Healthy, nil
}

func TestNewManagerRequiresDependencies(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}

	t.Run("nil reloader rejected", func(t *testing.T) {
		_, err := sip.NewManager(dialer, nil)
		if err == nil {
			t.Error("expected error for nil reloader, got nil")
		}
	})

	t.Run("nil dialer rejected", func(t *testing.T) {
		_, err := sip.NewManager(nil, reloader)
		if err == nil {
			t.Error("expected error for nil dialer, got nil")
		}
	})

	t.Run("valid manager instantiation", func(t *testing.T) {
		mgr, err := sip.NewManager(dialer, reloader)
		if err != nil {
			t.Fatalf("unexpected error during manager instantiation: %v", err)
		}
		if mgr == nil {
			t.Fatal("expected non-nil manager")
		}
	})
}

func TestPortValidationCases(t *testing.T) {
	t.Run("port 0 defaults to 5060", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.invalid", Port: 0, AuthType: sip.AuthIP}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error on port 0: %v", err)
		}
		if cfg.Port != 5060 {
			t.Errorf("expected port to default to 5060, got: %d", cfg.Port)
		}
	})

	t.Run("negative port rejected", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.invalid", Port: -1, AuthType: sip.AuthIP}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for negative port -1, got nil")
		}
	})

	t.Run("valid max port 65535", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.invalid", Port: 65535, AuthType: sip.AuthIP}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected port 65535 to be valid, got: %v", err)
		}
	})

	t.Run("port 65536 rejected", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.invalid", Port: 65536, AuthType: sip.AuthIP}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for port 65536, got nil")
		}
	})
}

func TestPJSIPInjectionRejection(t *testing.T) {
	badInputs := []string{
		"trunk\n[malicious-section]",
		"user\rpassword=injected",
		"host[injected]",
	}

	for _, input := range badInputs {
		cfg := sip.TrunkConfig{
			Name:         "validname",
			Host:         "sip.example.invalid",
			AuthType:     sip.AuthUserPass,
			AuthUsername: input,
			Secret:       "pass",
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected injection error for input %q, got nil", input)
		}
	}
}

func TestPathTraversalRejection(t *testing.T) {
	badNames := []string{
		"../trunk",
		"../../etc/passwd",
		"foo/bar",
		"foo\\bar",
		"spaces in name",
		"cr\ninjection",
		"[section]",
		"-invalidstart",
		".invalidstart",
	}

	for _, badName := range badNames {
		cfg := sip.TrunkConfig{
			Name:     badName,
			Host:     "sip.example.invalid",
			AuthType: sip.AuthIP,
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected path traversal/allowlist validation error for trunk name %q, got nil", badName)
		}
	}
}

func TestRegistrationIdentityValidation(t *testing.T) {
	t.Run("missing identity when registration required", func(t *testing.T) {
		cfg := sip.TrunkConfig{
			Name:                 "noidentity",
			Host:                 "sip.example.invalid",
			AuthType:             sip.AuthIP,
			RegistrationRequired: true,
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error when registration required without identity, got nil")
		}
	})

	t.Run("valid registration with from_user identity", func(t *testing.T) {
		cfg := sip.TrunkConfig{
			Name:                 "fromuseridentity",
			Host:                 "sip.example.invalid",
			AuthType:             sip.AuthIP,
			FromUser:             "trunk_user_123",
			RegistrationRequired: true,
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected valid config with from_user identity, got: %v", err)
		}

		pjsipConf, err := sip.GeneratePJSIPConfig(cfg)
		if err != nil {
			t.Fatalf("pjsip generation failed: %v", err)
		}
		if strings.Contains(pjsipConf, "sip:@") {
			t.Errorf("invalid client_uri generated: sip:@ found in:\n%s", pjsipConf)
		}
	})
}

func TestSecretMaskingWhitespace(t *testing.T) {
	secretWithSpace := "top secret password 123"
	cfg := sip.TrunkConfig{
		Name:         "secrettest",
		Host:         "sip.example.invalid",
		AuthType:     sip.AuthUserPass,
		AuthUsername: "user",
		Secret:       secretWithSpace,
	}

	str := cfg.String()
	if strings.Contains(str, "top secret") {
		t.Fatalf("CRITICAL SECURITY RISK: Secret leaked in String(): %s", str)
	}

	pjsipConf := "username=user\npassword=top secret password 123\nrealm=sip"
	masked := sip.MaskPJSIPSecrets(pjsipConf)
	if strings.Contains(masked, "top secret") {
		t.Fatalf("CRITICAL SECURITY RISK: Secret leaked in MaskPJSIPSecrets(): %s", masked)
	}
	if !strings.Contains(masked, "password=*****") {
		t.Errorf("expected password=***** in masked output, got: %s", masked)
	}
}

func TestTLSReachabilityAndFailure(t *testing.T) {
	t.Run("TLS reachability success", func(t *testing.T) {
		dialer := &mockDialer{}
		reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:      "tlstrunk",
			Host:      "sip.example.invalid",
			Port:      5061,
			Transport: sip.TransportTLS,
			AuthType:  sip.AuthIP,
			Enabled:   true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err != nil {
			t.Fatalf("expected TLS apply success, got: %v", err)
		}
		if status.Status != sip.StatusReady {
			t.Errorf("expected status READY for TLS, got: %s", status.Status)
		}
	})

	t.Run("TLS handshake failure", func(t *testing.T) {
		dialer := &mockDialer{tlsErr: errors.New("tls handshake certificate expired")}
		reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:      "tlsfailedtrunk",
			Host:      "sip.example.invalid",
			Port:      5061,
			Transport: sip.TransportTLS,
			AuthType:  sip.AuthIP,
			Enabled:   true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Error("expected error on TLS handshake failure, got nil")
		}
		if status.Status != sip.StatusConnectionError {
			t.Errorf("expected status CONNECTION_ERROR for TLS failure, got: %s", status.Status)
		}
	})
}

func TestDisableTrunkLifecycle(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	// Step 1: Enable trunk
	cfg := sip.TrunkConfig{
		Name:     "activetrunk",
		Host:     "sip.example.invalid",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}
	ctx := context.Background()
	_, err := mgr.ApplyTrunk(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to apply initial trunk: %v", err)
	}

	// Step 2: Disable trunk
	cfgDisabled := cfg
	cfgDisabled.Enabled = false
	status, err := mgr.ApplyTrunk(ctx, cfgDisabled)
	if err != nil {
		t.Fatalf("expected successful disable, got: %v", err)
	}

	if !reloader.RemoveCalled {
		t.Error("expected RemovePJSIPConfig to be called on Asterisk when disabling trunk")
	}
	if status.Status != sip.StatusDisabled {
		t.Errorf("expected status DISABLED, got: %s", status.Status)
	}
	if status.EndpointActive {
		t.Error("expected endpoint_active to be false when disabled")
	}
}

func TestRegistrationFailures(t *testing.T) {
	t.Run("registration rejected", func(t *testing.T) {
		dialer := &mockDialer{}
		reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true, RegistrationState: "Rejected"}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:                 "rejectedtrunk",
			Host:                 "sip.example.invalid",
			AuthType:             sip.AuthUserPass,
			AuthUsername:         "user",
			Secret:               "pass",
			RegistrationRequired: true,
			Enabled:              true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Error("expected error for rejected registration, got nil")
		}
		if status.Status != sip.StatusRegistrationFailed {
			t.Errorf("expected REGISTRATION_FAILED status, got: %s", status.Status)
		}
		if status.EndpointActive {
			t.Error("expected EndpointActive to be false on registration failure")
		}
		if !reloader.RollbackCalled {
			t.Error("expected RollbackPJSIPConfig to be called when registration fails")
		}
	})

	t.Run("registration error", func(t *testing.T) {
		dialer := &mockDialer{}
		reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true, RegistrationState: "Failed"}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:                 "failedtrunk",
			Host:                 "sip.example.invalid",
			AuthType:             sip.AuthUserPass,
			AuthUsername:         "user",
			Secret:               "pass",
			RegistrationRequired: true,
			Enabled:              true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Error("expected error for failed registration, got nil")
		}
		if status.Status != sip.StatusRegistrationFailed {
			t.Errorf("expected REGISTRATION_FAILED status, got: %s", status.Status)
		}
	})
}

func TestAsteriskHealthAndEndpointChecks(t *testing.T) {
	t.Run("unhealthy asterisk triggers rollback and error", func(t *testing.T) {
		dialer := &mockDialer{}
		reloader := &MockAsteriskReloader{Healthy: false, HealthErr: errors.New("asterisk service dead")}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:     "unhealthytrunk",
			Host:     "sip.example.invalid",
			AuthType: sip.AuthIP,
			Enabled:  true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Error("expected error when Asterisk is unhealthy, got nil")
		}
		if !reloader.RollbackCalled {
			t.Error("expected RollbackPJSIPConfig to be called when Asterisk is unhealthy")
		}
		if status.Status != sip.StatusConnectionError {
			t.Errorf("expected STATUS_CONNECTION_ERROR, got %s", status.Status)
		}
	})

	t.Run("endpoint missing in Asterisk triggers rollback and error", func(t *testing.T) {
		dialer := &mockDialer{}
		reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: false, EndpointErr: errors.New("endpoint not found")}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:     "missingendpoint",
			Host:     "sip.example.invalid",
			AuthType: sip.AuthIP,
			Enabled:  true,
		}

		status, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Error("expected error when endpoint is missing, got nil")
		}
		if !reloader.RollbackCalled {
			t.Error("expected RollbackPJSIPConfig to be called when endpoint is missing")
		}
		if status.Status != sip.StatusConfigured {
			t.Errorf("expected STATUS_CONFIGURED, got %s", status.Status)
		}
	})
}

func TestNonRegistrationTrunk(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	cfg := sip.TrunkConfig{
		Name:                 "ipauthtrunk",
		Host:                 "sip.example.invalid",
		AuthType:             sip.AuthIP,
		RegistrationRequired: false,
		Enabled:              true,
	}

	status, err := mgr.ApplyTrunk(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected non-registration trunk to succeed, got: %v", err)
	}
	if status.Status != sip.StatusReady {
		t.Errorf("expected status READY for non-registration trunk, got: %s", status.Status)
	}
	if !status.EndpointActive {
		t.Error("expected EndpointActive to be true")
	}
	if status.RegistrationState != "N/A" {
		t.Errorf("expected RegistrationState N/A, got: %s", status.RegistrationState)
	}
}

func TestCodecDefensiveCopy(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true, EndpointActive: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	originalCodecs := []string{"ulaw", "alaw"}
	cfg := sip.TrunkConfig{
		Name:     "codectest",
		Host:     "sip.example.invalid",
		AuthType: sip.AuthIP,
		Codecs:   originalCodecs,
		Enabled:  true,
	}

	_, err := mgr.ApplyTrunk(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to apply trunk: %v", err)
	}

	// Mutate caller's slice
	originalCodecs[0] = "g729_injected"

	storedCfg, _ := mgr.GetTrunk("codectest")
	if storedCfg.Codecs[0] == "g729_injected" {
		t.Fatal("CRITICAL DEFENSIVE COPY FAILURE: internal state was mutated via caller slice modification!")
	}
}

type mockRunner struct {
	failReload     bool
	failRollback   bool
	reloadCalls    int
	statusOutput   string
	endpointOutput string
	regOutput      string
}

func (r *mockRunner) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmdStr := strings.Join(args, " ")
	if strings.Contains(cmdStr, "core show version") || strings.Contains(cmdStr, "core show status") || strings.Contains(cmdStr, "core show uptime") {
		if r.statusOutput != "" {
			return r.statusOutput, nil
		}
		return "Asterisk 20.5.0", nil
	}
	if strings.Contains(cmdStr, "pjsip show transport") {
		return "Transport: transport-udp/udp", nil
	}
	if strings.Contains(cmdStr, "pjsip show endpoint") {
		if r.endpointOutput != "" {
			return r.endpointOutput, nil
		}
		return "Endpoint: trunk-testtrunk/Unregistered", nil
	}
	if strings.Contains(cmdStr, "pjsip show registration") {
		if r.regOutput != "" {
			return r.regOutput, nil
		}
		return "Objects found: 1 Registered", nil
	}
	if strings.Contains(cmdStr, "module reload res_pjsip.so") {
		r.reloadCalls++
		if r.failReload || (r.reloadCalls > 1 && r.failRollback) {
			return "Module reload failed", errors.New("reload error")
		}
		return "Module reload succeeded", nil
	}
	return "", nil
}

func TestRealAsteriskReloaderTransactionalRollbackCases(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pjsip-transaction-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dialer := &mockDialer{}

	// Caso 1 — health failure with old config functional
	t.Run("Caso 1 - health failure restores old config", func(t *testing.T) {
		runner := &mockRunner{statusOutput: "Unable to connect to remote PBX daemon"}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)
		mgr, _ := sip.NewManager(dialer, reloader)

		targetFile := filepath.Join(tmpDir, "trunk1.conf")
		oldContent := "; OLD FUNCTIONAL CONFIG\n[trunk-trunk1]\ntype=endpoint\n"
		if err := os.WriteFile(targetFile, []byte(oldContent), 0600); err != nil {
			t.Fatalf("failed to prepare old config: %v", err)
		}

		cfg := sip.TrunkConfig{Name: "trunk1", Host: "sip.example.invalid", AuthType: sip.AuthIP, Enabled: true}
		_, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error on health failure, got nil")
		}

		// Verify old config was restored on filesystem
		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("expected old config file to be restored on disk: %v", err)
		}
		if !strings.Contains(string(content), "OLD FUNCTIONAL CONFIG") {
			t.Errorf("expected old config content to be restored, got: %s", string(content))
		}
	})

	// Caso 2 — endpoint failure restores old config
	t.Run("Caso 2 - endpoint failure restores old config", func(t *testing.T) {
		runner := &mockRunner{endpointOutput: "Unable to find object"}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)
		mgr, _ := sip.NewManager(dialer, reloader)

		targetFile := filepath.Join(tmpDir, "trunk2.conf")
		oldContent := "; OLD FUNCTIONAL CONFIG TRUNK 2\n"
		_ = os.WriteFile(targetFile, []byte(oldContent), 0600)

		cfg := sip.TrunkConfig{Name: "trunk2", Host: "sip.example.invalid", AuthType: sip.AuthIP, Enabled: true}
		_, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error on endpoint failure, got nil")
		}

		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("expected old config file to be restored: %v", err)
		}
		if !strings.Contains(string(content), "OLD FUNCTIONAL CONFIG TRUNK 2") {
			t.Errorf("expected old config restored, got: %s", string(content))
		}
	})

	// Caso 3 — registration failure restores old config
	t.Run("Caso 3 - registration failure restores old config", func(t *testing.T) {
		runner := &mockRunner{regOutput: "Objects found: 0 Rejected"}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)
		mgr, _ := sip.NewManager(dialer, reloader)

		targetFile := filepath.Join(tmpDir, "trunk3.conf")
		oldContent := "; OLD FUNCTIONAL CONFIG TRUNK 3\n"
		_ = os.WriteFile(targetFile, []byte(oldContent), 0600)

		cfg := sip.TrunkConfig{
			Name:                 "trunk3",
			Host:                 "sip.example.invalid",
			AuthType:             sip.AuthUserPass,
			AuthUsername:         "user",
			Secret:               "pass",
			RegistrationRequired: true,
			Enabled:              true,
		}
		_, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error on registration failure, got nil")
		}

		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("expected old config file to be restored: %v", err)
		}
		if !strings.Contains(string(content), "OLD FUNCTIONAL CONFIG TRUNK 3") {
			t.Errorf("expected old config restored, got: %s", string(content))
		}
	})

	// Caso 4 — primeira configuração sem old config
	t.Run("Caso 4 - first config without old config removes new file on failure", func(t *testing.T) {
		runner := &mockRunner{endpointOutput: "Unable to find object"}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)
		mgr, _ := sip.NewManager(dialer, reloader)

		targetFile := filepath.Join(tmpDir, "newtrunk.conf")

		cfg := sip.TrunkConfig{Name: "newtrunk", Host: "sip.example.invalid", AuthType: sip.AuthIP, Enabled: true}
		_, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error on endpoint failure for new trunk, got nil")
		}

		// Verify targetFile is completely removed
		if _, err := os.Stat(targetFile); err == nil {
			t.Error("expected target config file to be removed when no old config existed")
		}
	})

	// Caso 5 — rollback failure returns compound error
	t.Run("Caso 5 - rollback failure returns compound error", func(t *testing.T) {
		runner := &mockRunner{statusOutput: "Unable to connect to remote PBX daemon", failRollback: true}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{Name: "rollbackfail", Host: "sip.example.invalid", AuthType: sip.AuthIP, Enabled: true}
		report, err := mgr.ApplyTrunk(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected compound error on rollback failure, got nil")
		}
		if !strings.Contains(err.Error(), "rollback failed") {
			t.Errorf("expected compound error containing 'rollback failed', got: %v", err)
		}
		if !strings.Contains(report.LastError, "rollback error") {
			t.Errorf("expected report.LastError to contain rollback error details, got: %s", report.LastError)
		}
	})

	// Caso 6 — initial reload failure without previous config and rollback failure returns compound error
	t.Run("Caso 6 - initial reload failure without previous config and rollback failure returns compound error", func(t *testing.T) {
		runner := &mockRunner{failReload: true, failRollback: true}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)

		cfg := sip.TrunkConfig{Name: "noreprevfail", Host: "sip.example.invalid", AuthType: sip.AuthIP, Enabled: true}
		pjsipConf, err := sip.GeneratePJSIPConfig(cfg)
		if err != nil {
			t.Fatalf("failed to generate pjsip config: %v", err)
		}

		err = reloader.StagePJSIPConfig(context.Background(), cfg.Name, pjsipConf)
		if err == nil {
			t.Fatal("expected error on initial reload failure without previous config, got nil")
		}
		if !strings.Contains(err.Error(), "asterisk reload failed during trunk stage") {
			t.Errorf("expected error to contain stage reload error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "rollback failed") {
			t.Errorf("expected compound error containing 'rollback failed', got: %v", err)
		}
	})
}

type concurrentTestReloader struct {
	base         sip.AsteriskReloader
	enteredStage chan string
	proceedStage chan struct{}
}

func newConcurrentTestReloader(base sip.AsteriskReloader) *concurrentTestReloader {
	return &concurrentTestReloader{
		base:         base,
		enteredStage: make(chan string, 10),
		proceedStage: make(chan struct{}, 10),
	}
}

func (r *concurrentTestReloader) allowStage() {
	r.proceedStage <- struct{}{}
}

func (r *concurrentTestReloader) StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	r.enteredStage <- trunkName
	<-r.proceedStage
	return r.base.StagePJSIPConfig(ctx, trunkName, pjsipConf)
}

func (r *concurrentTestReloader) CommitPJSIPConfig(ctx context.Context, trunkName string) error {
	return r.base.CommitPJSIPConfig(ctx, trunkName)
}
func (r *concurrentTestReloader) RollbackPJSIPConfig(ctx context.Context, trunkName string) error {
	return r.base.RollbackPJSIPConfig(ctx, trunkName)
}

func (r *concurrentTestReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	return r.base.RemovePJSIPConfig(ctx, trunkName)
}
func (r *concurrentTestReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	return r.base.CheckAsteriskHealth(ctx)
}
func (r *concurrentTestReloader) CheckTransport(ctx context.Context, transport sip.TransportType) (bool, error) {
	return r.base.CheckTransport(ctx, transport)
}
func (r *concurrentTestReloader) CheckEndpoint(ctx context.Context, trunkName string) (bool, error) {
	return r.base.CheckEndpoint(ctx, trunkName)
}
func (r *concurrentTestReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	return r.base.CheckRegistration(ctx, trunkName)
}

func TestSameTrunkConcurrentSerialization(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pjsip-concurrency-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dialer := &mockDialer{}
	runner := &mockRunner{}
	realReloader := sip.NewRealAsteriskReloader(tmpDir, runner)
	testReloader := newConcurrentTestReloader(realReloader)

	mgr, err := sip.NewManager(dialer, testReloader)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	trunkName := "trunksame"
	cfgV1 := sip.TrunkConfig{
		Name:     trunkName,
		Host:     "10.0.0.1",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}
	cfgV2 := sip.TrunkConfig{
		Name:     trunkName,
		Host:     "10.0.0.2",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}

	// Goroutine 1 starts ApplyTrunk for trunkV1
	go1Done := make(chan error, 1)
	go func() {
		_, applyErr := mgr.ApplyTrunk(context.Background(), cfgV1)
		go1Done <- applyErr
	}()

	// Wait deterministically for Goroutine 1 to reach StagePJSIPConfig
	firstStaged := <-testReloader.enteredStage
	if firstStaged != trunkName {
		t.Fatalf("expected first staged trunk to be %s, got %s", trunkName, firstStaged)
	}

	// While Goroutine 1 is blocked in stage (holding per-trunk mutex and globalReloadMu),
	// start Goroutine 2 for the SAME trunk (trunkV2)
	go2Done := make(chan error, 1)
	go func() {
		_, applyErr := mgr.ApplyTrunk(context.Background(), cfgV2)
		go2Done <- applyErr
	}()

	// Also test an independent trunk
	otherTrunkName := "trunkother"
	cfgOther := sip.TrunkConfig{
		Name:     otherTrunkName,
		Host:     "10.0.0.3",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}
	goOtherDone := make(chan error, 1)
	go func() {
		_, applyErr := mgr.ApplyTrunk(context.Background(), cfgOther)
		goOtherDone <- applyErr
	}()

	// Release Goroutine 1 so it can complete commit and release locks
	testReloader.allowStage()
	if err := <-go1Done; err != nil {
		t.Fatalf("goroutine 1 apply failed: %v", err)
	}

	// After Goroutine 1 finishes, remaining queued operations (go2 and goOther) proceed in order
	for i := 0; i < 2; i++ {
		<-testReloader.enteredStage
		testReloader.allowStage()
	}

	if err := <-go2Done; err != nil {
		t.Fatalf("goroutine 2 apply failed: %v", err)
	}
	if err := <-goOtherDone; err != nil {
		t.Fatalf("goroutine other apply failed: %v", err)
	}

	// Verify final disk state consistency
	targetFile := filepath.Join(tmpDir, trunkName+".conf")
	tmpFile := targetFile + ".tmp"
	bakFile := targetFile + ".bak"

	if _, err := os.Stat(tmpFile); err == nil {
		t.Errorf("residual temp file %s exists on disk after completion", tmpFile)
	}
	if _, err := os.Stat(bakFile); err == nil {
		t.Errorf("residual backup file %s exists on disk after completion", bakFile)
	}

	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed to read final config file: %v", err)
	}
	if !strings.Contains(string(content), "10.0.0.2") {
		t.Errorf("expected final config file to contain V2 host 10.0.0.2, got: %s", string(content))
	}

	// Verify Manager internal state consistency
	storedCfg, ok := mgr.GetTrunk(trunkName)
	if !ok || storedCfg.Host != "10.0.0.2" {
		t.Errorf("expected manager stored config to be V2 (10.0.0.2), got: %+v", storedCfg)
	}
}

func TestPJSIPSharedTransportConfigGeneration(t *testing.T) {
	cfgUDP1 := sip.TrunkConfig{Name: "udp1", Host: "10.0.0.1", Transport: sip.TransportUDP, AuthType: sip.AuthIP, Enabled: true}
	cfgUDP2 := sip.TrunkConfig{Name: "udp2", Host: "10.0.0.2", Transport: sip.TransportUDP, AuthType: sip.AuthIP, Enabled: true}
	cfgTCP1 := sip.TrunkConfig{Name: "tcp1", Host: "10.0.0.3", Transport: sip.TransportTCP, AuthType: sip.AuthIP, Enabled: true}

	outUDP1, err := sip.GeneratePJSIPConfig(cfgUDP1)
	if err != nil {
		t.Fatalf("failed to generate UDP1 config: %v", err)
	}
	outUDP2, err := sip.GeneratePJSIPConfig(cfgUDP2)
	if err != nil {
		t.Fatalf("failed to generate UDP2 config: %v", err)
	}
	outTCP1, err := sip.GeneratePJSIPConfig(cfgTCP1)
	if err != nil {
		t.Fatalf("failed to generate TCP1 config: %v", err)
	}

	// Verify no type=transport section is defined inside per-trunk snippets
	for _, out := range []string{outUDP1, outUDP2, outTCP1} {
		if strings.Contains(out, "type=transport") {
			t.Errorf("PJSIP trunk snippet MUST NOT contain type=transport section: %s", out)
		}
		if strings.Contains(out, "[transport-udp]") || strings.Contains(out, "[transport-tcp]") || strings.Contains(out, "[transport-tls]") {
			t.Errorf("PJSIP trunk snippet MUST NOT define transport headers: %s", out)
		}
	}

	// Verify endpoints reference shared transport
	if !strings.Contains(outUDP1, "transport=transport-udp") {
		t.Errorf("UDP1 endpoint must reference transport=transport-udp, got: %s", outUDP1)
	}
	if !strings.Contains(outUDP2, "transport=transport-udp") {
		t.Errorf("UDP2 endpoint must reference transport=transport-udp, got: %s", outUDP2)
	}
	if !strings.Contains(outTCP1, "transport=transport-tcp") {
		t.Errorf("TCP1 endpoint must reference transport=transport-tcp, got: %s", outTCP1)
	}
}

func TestCheckTransportFailClosed(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true, TransportInactive: true, EndpointActive: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	cfg := sip.TrunkConfig{Name: "notransport", Host: "sip.example.invalid", AuthType: sip.AuthIP, Transport: sip.TransportUDP, Enabled: true}
	report, err := mgr.ApplyTrunk(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when shared transport is missing from Asterisk, got nil")
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("expected transport check error, got: %v", err)
	}
	if report.Status == sip.StatusReady {
		t.Errorf("trunk status MUST NOT reach READY when shared transport is missing, got: %s", report.Status)
	}
}

func TestGlobalAsteriskTransactionLock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pjsip-global-lock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dialer := &mockDialer{}
	runner := &mockRunner{}
	realReloader := sip.NewRealAsteriskReloader(tmpDir, runner)
	testReloader := newConcurrentTestReloader(realReloader)

	mgr, err := sip.NewManager(dialer, testReloader)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	cfgA := sip.TrunkConfig{Name: "trunka", Host: "10.0.0.1", AuthType: sip.AuthIP, Enabled: true}
	cfgB := sip.TrunkConfig{Name: "trunkb", Host: "10.0.0.2", AuthType: sip.AuthIP, Enabled: true}

	goADone := make(chan error, 1)
	go func() {
		_, applyErr := mgr.ApplyTrunk(context.Background(), cfgA)
		goADone <- applyErr
	}()

	// Wait for Trunk A to enter stage
	stagedA := <-testReloader.enteredStage
	if stagedA != "trunka" {
		t.Fatalf("expected trunka to enter stage first, got %s", stagedA)
	}

	// While Trunk A holds globalReloadMu, start Trunk B (a DIFFERENT trunk)
	goBDone := make(chan error, 1)
	go func() {
		_, applyErr := mgr.ApplyTrunk(context.Background(), cfgB)
		goBDone <- applyErr
	}()

	// Verify Trunk B cannot enter stage because globalReloadMu is held by Trunk A
	select {
	case stagedB := <-testReloader.enteredStage:
		t.Fatalf("CRITICAL SECURITY VIOLATION: Trunk B entered stage (%s) while Trunk A held globalReloadMu!", stagedB)
	default:
		// Trunk B is properly waiting on globalReloadMu
	}

	// Release Trunk A
	testReloader.allowStage()
	if err := <-goADone; err != nil {
		t.Fatalf("trunk A apply failed: %v", err)
	}

	// Now Trunk B can acquire globalReloadMu and enter stage
	stagedB := <-testReloader.enteredStage
	if stagedB != "trunkb" {
		t.Fatalf("expected trunkb to enter stage second, got %s", stagedB)
	}
	testReloader.allowStage()
	if err := <-goBDone; err != nil {
		t.Fatalf("trunk B apply failed: %v", err)
	}
}


type mockRunnerFunc struct {
	runFunc func(ctx context.Context, name string, args ...string) (string, error)
}

func (m *mockRunnerFunc) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	if m.runFunc != nil {
		return m.runFunc(ctx, name, args...)
	}
	return "", nil
}

func TestFilesystemSecurityFailClosed(t *testing.T) {
	tempDir := t.TempDir()
	reloadCalled := false
	runner := &mockRunnerFunc{
		runFunc: func(ctx context.Context, name string, args ...string) (string, error) {
			reloadCalled = true
			return "success", nil
		},
	}

	reloader := sip.NewRealAsteriskReloader(tempDir, runner)
	reloader.SetChownFunc(func(name string, uid, gid int) error {
		return fmt.Errorf("chown permission denied: operation not permitted (EPERM)")
	})

	trunkName := "failclosedtest"
	pjsipConf := "[trunk-failclosedtest]\ntype=endpoint\n"

	err := reloader.StagePJSIPConfig(context.Background(), trunkName, pjsipConf)
	if err == nil {
		t.Fatalf("expected StagePJSIPConfig to fail closed when chown returns EPERM, got nil error")
	}
	if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "chown") {
		t.Fatalf("expected error message to mention chown/permission denied, got: %v", err)
	}

	if reloadCalled {
		t.Fatalf("security violation: Asterisk reload was executed after chown/security policy failure")
	}

	targetPath := filepath.Join(tempDir, trunkName+".conf")
	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Fatalf("security violation: target config file %s exists on disk after staging security failure", targetPath)
	}
}

func TestRollbackSecurityErrorPropagation(t *testing.T) {
	tempDir := t.TempDir()
	runner := &mockRunner{}
	reloader := sip.NewRealAsteriskReloader(tempDir, runner)

	trunkName := "rollbacksecerr"
	pjsipConf := "[trunk-rollbacksecerr]\ntype=endpoint\n"

	if err := reloader.StagePJSIPConfig(context.Background(), trunkName, pjsipConf); err != nil {
		t.Fatalf("failed to stage initial config: %v", err)
	}

	updatedConf := pjsipConf + "\n; update\n"
	if err := reloader.StagePJSIPConfig(context.Background(), trunkName, updatedConf); err != nil {
		t.Fatalf("failed to stage updated config: %v", err)
	}

	reloader.SetChownFunc(func(name string, uid, gid int) error {
		return fmt.Errorf("simulated chown failure during rollback")
	})

	err := reloader.RollbackPJSIPConfig(context.Background(), trunkName)
	if err == nil {
		t.Fatalf("expected RollbackPJSIPConfig to return compound error when security policy fails on restore, got nil")
	}
	if !strings.Contains(err.Error(), "secErr") && !strings.Contains(err.Error(), "simulated chown failure") {
		t.Fatalf("expected rollback error to preserve security error details, got: %v", err)
	}
}

func TestRealAsteriskIntegrationSmoke(t *testing.T) {
	if os.Getenv("ENABLE_REAL_ASTERISK_SMOKE") != "1" {
		t.Skip("skipping real Asterisk smoke test: ENABLE_REAL_ASTERISK_SMOKE != 1")
	}
	if _, err := exec.LookPath("asterisk"); err != nil {
		t.Skip("skipping real Asterisk smoke test: asterisk binary not in PATH")
	}

	configDir := os.Getenv("ASTERISK_PJSIP_TEST_DIR")
	if configDir == "" {
		t.Skip("skipping real Asterisk smoke test: ASTERISK_PJSIP_TEST_DIR environment variable is required")
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("unable to create test config directory %s: %v", configDir, err)
	}

	trunkName := fmt.Sprintf("smoketestreal_%d", time.Now().UnixNano())
	targetConfFile := filepath.Join(configDir, trunkName+".conf")
	tmpConfFile := targetConfFile + ".tmp"
	bakConfFile := targetConfFile + ".bak"

	if _, err := os.Stat(targetConfFile); err == nil {
		t.Fatalf("preflight failure: test config file %s already exists", targetConfFile)
	}
	if _, err := os.Stat(tmpConfFile); err == nil {
		t.Fatalf("preflight failure: test tmp file %s already exists", tmpConfFile)
	}
	if _, err := os.Stat(bakConfFile); err == nil {
		t.Fatalf("preflight failure: test bak file %s already exists", bakConfFile)
	}

	dialer := &mockDialer{}
	reloader := sip.NewRealAsteriskReloader(configDir, nil)
	mgr, err := sip.NewManager(dialer, reloader)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	cfg := sip.TrunkConfig{
		Name:                 trunkName,
		Host:                 "127.0.0.1",
		Port:                 5060,
		Transport:            sip.TransportUDP,
		AuthType:             sip.AuthIP,
		RegistrationRequired: false,
		Enabled:              true,
	}

	t.Cleanup(func() {
		ctx := context.Background()
		cfgDisable := cfg
		cfgDisable.Enabled = false
		_, _ = mgr.ApplyTrunk(ctx, cfgDisable)
		if err := reloader.RemovePJSIPConfig(ctx, trunkName); err != nil {
			t.Logf("cleanup warning: RemovePJSIPConfig returned error for %s: %v", trunkName, err)
		}

		runner := sip.OSCommandRunner{}
		if out, err := runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so"); err != nil {
			t.Logf("cleanup warning: Asterisk reload returned error: %v (output: %s)", err, out)
		}

		active, err := reloader.CheckEndpoint(ctx, trunkName)
		if active || err != nil {
			t.Errorf("cleanup verification failed: endpoint %s still active or check error: %v", trunkName, err)
		}

		for _, path := range []string{targetConfFile, tmpConfFile, bakConfFile} {
			if _, err := os.Stat(path); err == nil {
				t.Errorf("cleanup verification failed: residual file %s still exists", path)
				_ = os.Remove(path)
			}
		}
	})

	status, err := mgr.ApplyTrunk(context.Background(), cfg)
	if err != nil {
		t.Fatalf("failed to apply trunk in real Asterisk: %v", err)
	}
	if status.Status != sip.StatusReady {
		t.Fatalf("expected StatusReady, got %s (LastError: %s)", status.Status, status.LastError)
	}
	if !status.EndpointActive {
		t.Fatalf("expected EndpointActive = true")
	}

	info, err := os.Stat(targetConfFile)
	if err != nil {
		t.Fatalf("target PJSIP config file %s not found on disk: %v", targetConfFile, err)
	}
	if info.Mode().Perm()&0004 != 0 {
		t.Fatalf("security violation: PJSIP config file %s is world-readable (%04o)", targetConfFile, info.Mode().Perm())
	}

	cfgDisable := cfg
	cfgDisable.Enabled = false
	disableStatus, err := mgr.ApplyTrunk(context.Background(), cfgDisable)
	if err != nil {
		t.Fatalf("failed to disable trunk in real Asterisk: %v", err)
	}
	if disableStatus.Status != sip.StatusDisabled {
		t.Fatalf("expected StatusDisabled, got %s", disableStatus.Status)
	}

	if _, err := os.Stat(targetConfFile); err == nil {
		t.Fatalf("PJSIP config file %s still exists after disabling trunk", targetConfFile)
	}
}
