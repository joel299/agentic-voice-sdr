package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.ReadTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 {
		t.Fatal("HTTP timeouts must be positive")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
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
