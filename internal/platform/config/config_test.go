package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	t.Setenv("FALEPACO_AUDIOSOCKET_ENABLED", "false")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("OWNER_API_TOKEN", "")
	t.Setenv("BARESIP_CTRL_TCP_ADDR", "")
	t.Setenv("OUTBOUND_CALL_DESTINATION_ALLOWLIST", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "127.0.0.1:8080")
	}
	if cfg.BaresipCtrlTCPAddress != "127.0.0.1:4444" || len(cfg.OutboundCallAllowlist) != 0 || cfg.OwnerAPIToken != "" {
		t.Fatalf("unexpected outbound call defaults: %+v", cfg)
	}
	if cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 {
		t.Fatal("HTTP timeouts must be positive")
	}
}

func TestLoadRejectsLegacyAudioSocketRuntime(t *testing.T) {
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	t.Setenv("FALEPACO_AUDIOSOCKET_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("legacy AudioSocket/Asterisk runtime must not start in the Baresip local E2E")
	}
}

func TestLoadRejectsAsteriskRuntime(t *testing.T) {
	t.Setenv("FALEPACO_AUDIOSOCKET_ENABLED", "false")
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "/any/path")
	if _, err := Load(); err == nil {
		t.Fatal("ASTERISK_PJSIP_CONFIG_DIR must fail closed for the Baresip-only runtime")
	}
}

func TestLoadOutboundCallSettings(t *testing.T) {
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	t.Setenv("OWNER_API_TOKEN", "owner-token")
	t.Setenv("BARESIP_CTRL_TCP_ADDR", "127.0.0.1:4444")
	t.Setenv("OUTBOUND_CALL_DESTINATION_ALLOWLIST", " +5567981340687, , +14155550100 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerAPIToken != "owner-token" || cfg.BaresipCtrlTCPAddress != "127.0.0.1:4444" || len(cfg.OutboundCallAllowlist) != 2 || cfg.OutboundCallAllowlist[0] != "+5567981340687" {
		t.Fatalf("outbound call settings not loaded: %+v", cfg)
	}
}

func TestLoadRejectsWhitespaceInOwnerToken(t *testing.T) {
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	t.Setenv("OWNER_API_TOKEN", "owner token")
	if _, err := Load(); err == nil {
		t.Fatal("expected token validation error")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	for key, value := range map[string]string{
		"HTTP_ADDR":          "127.0.0.1:9090",
		"HTTP_READ_TIMEOUT":  "2s",
		"HTTP_WRITE_TIMEOUT": "3s",
		"HTTP_IDLE_TIMEOUT":  "4s",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.ReadTimeout != 2*time.Second || cfg.WriteTimeout != 3*time.Second || cfg.IdleTimeout != 4*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	_ = os.Getenv("HTTP_ADDR")
}
