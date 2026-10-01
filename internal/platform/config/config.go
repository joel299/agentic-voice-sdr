package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	HTTPAddr                   string
	WhatsAppConfigPath         string
	SIPConfigDir               string
	ReadTimeout                time.Duration
	WriteTimeout               time.Duration
	IdleTimeout                time.Duration
	ShutdownGrace              time.Duration
	FalePacoAudioSocketEnabled bool
	FalePacoAudioSocketAddr    string
	FalePacoRuntimeLogPath     string
	OwnerAPIToken              string
	BaresipCtrlTCPAddress      string
	BaresipBinaryPath          string
	BaresipProfileDir          string
	BaresipMediaModulePath     string
	BaresipSystemModuleDir     string
	OutboundCallAllowlist      []string
}

func Load() (Config, error) {
	if strings.TrimSpace(os.Getenv("ASTERISK_PJSIP_CONFIG_DIR")) != "" {
		return Config{}, fmt.Errorf("ASTERISK_PJSIP_CONFIG_DIR is unsupported; this runtime uses Baresip only")
	}
	enabled, err := envBool("FALEPACO_AUDIOSOCKET_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{HTTPAddr: envOrDefault("HTTP_ADDR", ":8080"), WhatsAppConfigPath: envOrDefault("WHATSAPP_CONFIG_PATH", ""), SIPConfigDir: "", ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, ShutdownGrace: 10 * time.Second, FalePacoAudioSocketEnabled: enabled, FalePacoAudioSocketAddr: envOrDefault("FALEPACO_AUDIOSOCKET_ADDR", "127.0.0.1:9092"), FalePacoRuntimeLogPath: envOrDefault("FALEPACO_RUNTIME_LOG_PATH", "/root/agentic-voice-sdr/.runtime-logs/falepaco/runtime.log"), OwnerAPIToken: os.Getenv("OWNER_API_TOKEN"), BaresipCtrlTCPAddress: envOrDefault("BARESIP_CTRL_TCP_ADDR", "127.0.0.1:4444"), BaresipBinaryPath: envOrDefault("BARESIP_BINARY_PATH", "/usr/bin/baresip"), BaresipProfileDir: envOrDefault("BARESIP_PROFILE_DIR", ""), BaresipMediaModulePath: envOrDefault("BARESIP_MEDIA_MODULE_PATH", ""), BaresipSystemModuleDir: envOrDefault("BARESIP_SYSTEM_MODULE_DIR", "/usr/lib/baresip/modules"), OutboundCallAllowlist: splitCSV(os.Getenv("OUTBOUND_CALL_DESTINATION_ALLOWLIST"))}
	if enabled {
		return Config{}, fmt.Errorf("AudioSocket runtime is disabled for the Baresip local E2E")
	}
	if strings.IndexFunc(cfg.OwnerAPIToken, unicode.IsSpace) >= 0 {
		return Config{}, fmt.Errorf("OWNER_API_TOKEN must not contain whitespace")
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{{"HTTP_READ_TIMEOUT", &cfg.ReadTimeout}, {"HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout}, {"HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout}, {"HTTP_SHUTDOWN_GRACE", &cfg.ShutdownGrace}} {
		if value := os.Getenv(item.name); value != "" {
			duration, e := time.ParseDuration(value)
			if e != nil || duration <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
			}
			*item.target = duration
		}
	}
	if cfg.FalePacoAudioSocketEnabled && cfg.FalePacoAudioSocketAddr == "" {
		return Config{}, fmt.Errorf("FALEPACO_AUDIOSOCKET_ADDR is required when AudioSocket is enabled")
	}
	return cfg, nil
}
func envBool(name string, fallback bool) (bool, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}
func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
