package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPAddr           string
	WhatsAppConfigPath string
	SIPConfigDir       string
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ShutdownGrace      time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           envOrDefault("HTTP_ADDR", ":8080"),
		WhatsAppConfigPath: envOrDefault("WHATSAPP_CONFIG_PATH", ""),
		SIPConfigDir:       envOrDefault("ASTERISK_PJSIP_CONFIG_DIR", ""),
		ReadTimeout:        10 * time.Second,
		WriteTimeout:       10 * time.Second,
		IdleTimeout:        60 * time.Second,
		ShutdownGrace:      10 * time.Second,
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{
		{"HTTP_READ_TIMEOUT", &cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", &cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", &cfg.IdleTimeout},
		{"HTTP_SHUTDOWN_GRACE", &cfg.ShutdownGrace},
	} {
		value := os.Getenv(item.name)
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration", item.name)
		}
		*item.target = duration
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
