package sip_test

import (
	"context"
	"errors"
	"net"
	"os"
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
	EndpointActive    bool
	RegistrationState string
	ReloadErr         error
	HealthErr         error
	EndpointErr       error
	RemoveCalled      bool
	ApplyCalled       bool
}

func (m *MockAsteriskReloader) ApplyPJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	m.ApplyCalled = true
	return m.ReloadErr
}

func (m *MockAsteriskReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	m.RemoveCalled = true
	return m.ReloadErr
}

func (m *MockAsteriskReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	if m.HealthErr != nil {
		return false, m.HealthErr
	}
	return m.Healthy, nil
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
		if !reloader.RemoveCalled {
			t.Error("expected RemovePJSIPConfig to be called for rollback when Asterisk is unhealthy")
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
		if !reloader.RemoveCalled {
			t.Error("expected RemovePJSIPConfig to be called for rollback when endpoint is missing")
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
	failReload bool
}

func (r *mockRunner) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmdStr := strings.Join(args, " ")
	if strings.Contains(cmdStr, "core show status") {
		return "Asterisk 20.5.0", nil
	}
	if strings.Contains(cmdStr, "pjsip show endpoint") {
		return "Endpoint: trunk-testtrunk/Unregistered", nil
	}
	if strings.Contains(cmdStr, "pjsip show registration") {
		return "Objects found: 1 Registered", nil
	}
	if strings.Contains(cmdStr, "module reload res_pjsip.so") {
		if r.failReload {
			return "Module reload failed", errors.New("reload error")
		}
		return "Module reload succeeded", nil
	}
	return "", nil
}

func TestRealAsteriskReloaderAtomicTransactions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pjsip-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Run("atomic apply success", func(t *testing.T) {
		runner := &mockRunner{}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)

		err := reloader.ApplyPJSIPConfig(context.Background(), "testtrunk", "[trunk-testtrunk]\ntype=endpoint\n")
		if err != nil {
			t.Fatalf("expected atomic apply to succeed, got: %v", err)
		}

		targetFile := filepath.Join(tmpDir, "testtrunk.conf")
		content, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("expected file %s to exist: %v", targetFile, err)
		}
		if !strings.Contains(string(content), "testtrunk") {
			t.Errorf("unexpected file content: %s", string(content))
		}
	})

	t.Run("atomic apply rollback on reload failure", func(t *testing.T) {
		runner := &mockRunner{failReload: true}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)

		err := reloader.ApplyPJSIPConfig(context.Background(), "failedtrunk", "[trunk-failedtrunk]\ntype=endpoint\n")
		if err == nil {
			t.Error("expected reload failure error, got nil")
		}

		targetFile := filepath.Join(tmpDir, "failedtrunk.conf")
		if _, err := os.Stat(targetFile); err == nil {
			t.Error("expected config file to be removed on reload failure rollback")
		}
	})

	t.Run("atomic disable rollback on failure", func(t *testing.T) {
		targetFile := filepath.Join(tmpDir, "disabletrunk.conf")
		_ = os.WriteFile(targetFile, []byte("existing config"), 0600)

		runner := &mockRunner{failReload: true}
		reloader := sip.NewRealAsteriskReloader(tmpDir, runner)

		err := reloader.RemovePJSIPConfig(context.Background(), "disabletrunk")
		if err == nil {
			t.Error("expected error on disable reload failure, got nil")
		}

		// Verify restored from backup
		if _, err := os.Stat(targetFile); err != nil {
			t.Error("expected config file to be restored after failed disable reload")
		}
	})
}
