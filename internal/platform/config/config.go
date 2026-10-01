package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	OwnerLoginUsername         string
	OwnerLoginPasswordHash     string
	OwnerSessionTTL            time.Duration
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
	hash := os.Getenv("OWNER_LOGIN_PASSWORD_HASH")
	if hash == "" {
		var readErr error
		hash, readErr = loadProtectedOwnerHash(filepath.Join(".runtime-secrets", "owner-login.env"))
		if readErr != nil && !os.IsNotExist(readErr) {
			return Config{}, fmt.Errorf("read protected owner login configuration")
		}
	}
	ttl := 8 * time.Hour
	if value := os.Getenv("OWNER_SESSION_TTL"); value != "" {
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("OWNER_SESSION_TTL must be a positive duration")
		}
		ttl = parsed
	}
	cfg := Config{HTTPAddr: envOrDefault("HTTP_ADDR", "127.0.0.1:8080"), WhatsAppConfigPath: envOrDefault("WHATSAPP_CONFIG_PATH", ""), SIPConfigDir: "", ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, ShutdownGrace: 10 * time.Second, FalePacoAudioSocketEnabled: enabled, FalePacoAudioSocketAddr: envOrDefault("FALEPACO_AUDIOSOCKET_ADDR", "127.0.0.1:9092"), FalePacoRuntimeLogPath: envOrDefault("FALEPACO_RUNTIME_LOG_PATH", "/root/agentic-voice-sdr/.runtime-logs/falepaco/runtime.log"), OwnerAPIToken: os.Getenv("OWNER_API_TOKEN"), OwnerLoginUsername: envOrDefault("OWNER_LOGIN_USERNAME", "owner"), OwnerLoginPasswordHash: hash, OwnerSessionTTL: ttl, BaresipCtrlTCPAddress: envOrDefault("BARESIP_CTRL_TCP_ADDR", "127.0.0.1:4444"), BaresipBinaryPath: envOrDefault("BARESIP_BINARY_PATH", "/usr/bin/baresip"), BaresipProfileDir: envOrDefault("BARESIP_PROFILE_DIR", ""), BaresipMediaModulePath: envOrDefault("BARESIP_MEDIA_MODULE_PATH", ""), BaresipSystemModuleDir: envOrDefault("BARESIP_SYSTEM_MODULE_DIR", "/usr/lib/baresip/modules"), OutboundCallAllowlist: splitCSV(os.Getenv("OUTBOUND_CALL_DESTINATION_ALLOWLIST"))}
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

func loadProtectedOwnerHash(path string) (string, error) {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("owner login config directory permissions are unsafe")
	}
	info, err = os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("owner login config file permissions are unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return parseLocalHash(string(data)), nil
}

func parseLocalHash(contents string) string {
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, "OWNER_LOGIN_PASSWORD_HASH=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "OWNER_LOGIN_PASSWORD_HASH="))
		}
	}
	return ""
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
