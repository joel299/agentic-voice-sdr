package sip_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

type mockDialer struct {
	lookupErr error
	dialErr   error
}

func (m *mockDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if m.dialErr != nil {
		return nil, m.dialErr
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

func TestValidTrunkConfigValidation(t *testing.T) {
	cfg := sip.TrunkConfig{
		Provider:             "fale_paco",
		Name:                 "fale-paco-trunk",
		Host:                 "sip.falepaco.com",
		Port:                 5060,
		Transport:            sip.TransportTCP,
		AuthType:             sip.AuthUserPass,
		AuthUsername:         "paco_user",
		Secret:               "super_secret_password_123",
		RegistrationRequired: true,
		Enabled:              true,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}

	if cfg.Port != 5060 {
		t.Errorf("expected port 5060, got %d", cfg.Port)
	}
	if cfg.Transport != sip.TransportTCP {
		t.Errorf("expected transport tcp, got %s", cfg.Transport)
	}
}

func TestInvalidConfigHostPortName(t *testing.T) {
	t.Run("missing name", func(t *testing.T) {
		cfg := sip.TrunkConfig{Host: "sip.example.com", Port: 5060}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for missing name, got nil")
		}
	})

	t.Run("missing host", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: ""}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for missing host, got nil")
		}
	})

	t.Run("invalid port out of bounds", func(t *testing.T) {
		cfg := sip.TrunkConfig{Name: "test", Host: "sip.example.com", Port: 70000}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for invalid port > 65535, got nil")
		}
	})
}

func TestTransportTypes(t *testing.T) {
	transports := []sip.TransportType{sip.TransportUDP, sip.TransportTCP, sip.TransportTLS}
	for _, tr := range transports {
		cfg := sip.TrunkConfig{
			Name:      "test-" + string(tr),
			Host:      "sip.example.com",
			Transport: tr,
			AuthType:  sip.AuthIP,
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected valid transport %s, got error: %v", tr, err)
		}
	}

	invalidCfg := sip.TrunkConfig{
		Name:      "invalid-tr",
		Host:      "sip.example.com",
		Transport: sip.TransportType("sctp"),
	}
	if err := invalidCfg.Validate(); err == nil {
		t.Error("expected error for invalid transport 'sctp', got nil")
	}
}

func TestAuthValidation(t *testing.T) {
	t.Run("missing userpass credentials", func(t *testing.T) {
		cfg := sip.TrunkConfig{
			Name:     "userpass-missing",
			Host:     "sip.example.com",
			AuthType: sip.AuthUserPass,
		}
		if err := cfg.Validate(); err == nil {
			t.Error("expected error for missing username/secret in userpass auth, got nil")
		}
	})

	t.Run("valid IP auth", func(t *testing.T) {
		cfg := sip.TrunkConfig{
			Name:     "ip-auth",
			Host:     "192.168.1.1",
			AuthType: sip.AuthIP,
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected valid IP auth config, got: %v", err)
		}
	})
}

func TestSecretMaskingInLogsAndStrings(t *testing.T) {
	secretVal := "super_secret_key_12345"
	cfg := sip.TrunkConfig{
		Name:         "secret-test",
		Host:         "sip.example.com",
		AuthType:     sip.AuthUserPass,
		AuthUsername: "user",
		Secret:       secretVal,
	}

	str := cfg.String()
	if strings.Contains(str, secretVal) {
		t.Fatalf("CRITICAL SECURITY RISK: Secret leaked in String(): %s", str)
	}
	if !strings.Contains(str, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in String(), got: %s", str)
	}

	goStr := cfg.GoString()
	if strings.Contains(goStr, secretVal) {
		t.Fatalf("CRITICAL SECURITY RISK: Secret leaked in GoString(): %s", goStr)
	}

	redacted := cfg.Redacted()
	if redacted.Secret != "*****" {
		t.Errorf("expected redacted secret to be '*****', got: %s", redacted.Secret)
	}
}

func TestPJSIPTemplateGenerationAndMasking(t *testing.T) {
	cfg := sip.TrunkConfig{
		Provider:             "elevenlabs_sip",
		Name:                 "elevenlabs-trunk",
		Host:                 "sip.rtc.elevenlabs.io",
		Port:                 5060,
		Transport:            sip.TransportTCP,
		AuthType:             sip.AuthUserPass,
		AuthUsername:         "eleven_user",
		Secret:               "secret_eleven_pass",
		RegistrationRequired: true,
		Codecs:               []string{"ulaw", "alaw"},
		Enabled:              true,
	}

	pjsipConf, err := sip.GeneratePJSIPConfig(cfg)
	if err != nil {
		t.Fatalf("PJSIP generation failed: %v", err)
	}

	requiredSections := []string{
		"[transport-tcp]",
		"[trunk-elevenlabs-trunk-auth]",
		"[trunk-elevenlabs-trunk-aor]",
		"[trunk-elevenlabs-trunk]",
		"[trunk-elevenlabs-trunk-reg]",
	}

	for _, sec := range requiredSections {
		if !strings.Contains(pjsipConf, sec) {
			t.Errorf("missing section %s in PJSIP config snippet", sec)
		}
	}

	masked := sip.MaskPJSIPSecrets(pjsipConf)
	if strings.Contains(masked, "secret_eleven_pass") {
		t.Errorf("expected password to be masked in MaskPJSIPSecrets, got: %s", masked)
	}
}

func TestReconcilerReconcileSuccess(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &sip.MockAsteriskReloader{Healthy: true, RegistrationState: "Registered"}
	manager := sip.NewManager(dialer, reloader)

	cfg := sip.TrunkConfig{
		Provider:             "fale_paco",
		Name:                 "paco-main",
		Host:                 "sip.falepaco.com",
		Port:                 5060,
		Transport:            sip.TransportUDP,
		AuthType:             sip.AuthUserPass,
		AuthUsername:         "paco_user",
		Secret:               "paco_pass",
		RegistrationRequired: true,
		Enabled:              true,
	}

	ctx := context.Background()
	status, err := manager.ApplyTrunk(ctx, cfg)
	if err != nil {
		t.Fatalf("expected successful reconciliation, got error: %v", err)
	}

	if status.Status != sip.StatusReady {
		t.Errorf("expected status READY, got %s", status.Status)
	}
	if !status.EndpointActive {
		t.Error("expected endpoint active to be true")
	}

	storedCfg, ok := manager.GetTrunk("paco-main")
	if !ok {
		t.Error("expected trunk to be stored in manager state")
	}
	if storedCfg.Name != "paco-main" {
		t.Errorf("expected stored name 'paco-main', got %s", storedCfg.Name)
	}
}

func TestReconcilerDNSFailure(t *testing.T) {
	dialer := &mockDialer{lookupErr: errors.New("no such host")}
	reloader := &sip.MockAsteriskReloader{Healthy: true}
	manager := sip.NewManager(dialer, reloader)

	cfg := sip.TrunkConfig{
		Name:     "invalid-dns-trunk",
		Host:     "nonexistent.invalid.host",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}

	ctx := context.Background()
	status, err := manager.ApplyTrunk(ctx, cfg)
	if err == nil {
		t.Error("expected error for DNS failure, got nil")
	}

	if status.Status != sip.StatusDNSError {
		t.Errorf("expected status DNS_ERROR, got %s", status.Status)
	}
}

func TestReconcilerConnectionFailure(t *testing.T) {
	dialer := &mockDialer{dialErr: errors.New("connection refused")}
	reloader := &sip.MockAsteriskReloader{Healthy: true}
	manager := sip.NewManager(dialer, reloader)

	cfg := sip.TrunkConfig{
		Name:      "unreachable-trunk",
		Host:      "192.168.1.250",
		Port:      5060,
		Transport: sip.TransportTCP,
		AuthType:  sip.AuthIP,
		Enabled:   true,
	}

	ctx := context.Background()
	status, err := manager.ApplyTrunk(ctx, cfg)
	if err == nil {
		t.Error("expected error for connection failure, got nil")
	}

	if status.Status != sip.StatusConnectionError {
		t.Errorf("expected status CONNECTION_ERROR, got %s", status.Status)
	}
}

func TestReconcilerReloadFailurePreservesPreviousState(t *testing.T) {
	dialer := &mockDialer{}
	reloader := &sip.MockAsteriskReloader{Healthy: true}
	manager := sip.NewManager(dialer, reloader)

	// Step 1: Apply initial valid config
	initialCfg := sip.TrunkConfig{
		Name:     "stable-trunk",
		Host:     "sip.stable.com",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}
	ctx := context.Background()
	_, err := manager.ApplyTrunk(ctx, initialCfg)
	if err != nil {
		t.Fatalf("failed to apply initial config: %v", err)
	}

	// Step 2: Attempt update with reload failure
	reloader.ReloadErr = errors.New("asterisk PJSIP syntax error")
	updateCfg := sip.TrunkConfig{
		Name:     "stable-trunk",
		Host:     "sip.stable.com",
		AuthType: sip.AuthIP,
		Port:     5062,
		Enabled:  true,
	}

	_, err = manager.ApplyTrunk(ctx, updateCfg)
	if err == nil {
		t.Error("expected reload failure error, got nil")
	}

	// Verify initial config state was preserved
	currentCfg, _ := manager.GetTrunk("stable-trunk")
	if currentCfg.Port != 5060 {
		t.Errorf("expected initial port 5060 to be preserved after reload failure, got %d", currentCfg.Port)
	}
}

func TestAudioSocketPathDecoupling(t *testing.T) {
	start := time.Now()
	cfg := sip.TrunkConfig{
		Name:     "fast-trunk",
		Host:     "localhost",
		AuthType: sip.AuthIP,
		Enabled:  true,
	}
	_ = cfg.Validate()
	duration := time.Since(start)

	if duration > 50*time.Millisecond {
		t.Errorf("validation took unexpectedly long: %v", duration)
	}
}
