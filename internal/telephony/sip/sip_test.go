package sip_test

import (
	"context"
	"errors"
	"net"
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
	RegistrationState string
	ReloadErr         error
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
	reloader := &MockAsteriskReloader{Healthy: true}

	t.Run("nil reloader rejected", func(t *testing.T) {
		_, err := sip.NewManager(dialer, nil)
		if err == nil {
			t.Error("expected error when reloader is nil, got nil")
		}
	})

	t.Run("nil dialer rejected", func(t *testing.T) {
		_, err := sip.NewManager(nil, reloader)
		if err == nil {
			t.Error("expected error when dialer is nil, got nil")
		}
	})

	t.Run("valid manager instantiation", func(t *testing.T) {
		mgr, err := sip.NewManager(dialer, reloader)
		if err != nil || mgr == nil {
			t.Fatalf("expected valid manager, got err: %v", err)
		}
	})
}

func TestPortValidationCases(t *testing.T) {
	t.Run("port 0 defaults to 5060", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.invalid", Port: 0, AuthType: sip.AuthIP}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected port 0 to be valid, got: %v", err)
		}
		if cfg.Port != 5060 {
			t.Errorf("expected default port 5060, got %d", cfg.Port)
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
			Name:         input,
			Host:         "sip.example.invalid",
			AuthType:     sip.AuthUserPass,
			AuthUsername: "user",
			Secret:       "pass",
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("expected injection error for input %q, got nil", input)
		}
	}
}

func TestRegistrationIdentityValidation(t *testing.T) {
	t.Run("missing identity when registration required", func(t *testing.T) {
		cfg := sip.TrunkConfig{
			Name:                 "no-identity",
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
			Name:                 "from-user-identity",
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
		Name:         "secret-test",
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
		reloader := &MockAsteriskReloader{Healthy: true}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:      "tls-trunk",
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
		reloader := &MockAsteriskReloader{Healthy: true}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:      "tls-failed-trunk",
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
	reloader := &MockAsteriskReloader{Healthy: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	// Step 1: Enable trunk
	cfg := sip.TrunkConfig{
		Name:     "active-trunk",
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
		reloader := &MockAsteriskReloader{Healthy: true, RegistrationState: "Rejected"}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:                 "rejected-trunk",
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
		reloader := &MockAsteriskReloader{Healthy: true, RegistrationState: "Failed"}
		mgr, _ := sip.NewManager(dialer, reloader)

		cfg := sip.TrunkConfig{
			Name:                 "failed-trunk",
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

func TestCodecDefensiveCopy(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &MockAsteriskReloader{Healthy: true}
	mgr, _ := sip.NewManager(dialer, reloader)

	originalCodecs := []string{"ulaw", "alaw"}
	cfg := sip.TrunkConfig{
		Name:     "codec-test",
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

	storedCfg, _ := mgr.GetTrunk("codec-test")
	if storedCfg.Codecs[0] == "g729_injected" {
		t.Fatal("CRITICAL DEFENSIVE COPY FAILURE: internal state was mutated via caller slice modification!")
	}
}
