package config_test

import (
	"bytes"
	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedLocalEnvRestartPreservesPolicy(t *testing.T) {
	for _, k := range config.RequiredLocalRuntimeVariables {
		t.Setenv(k, "stale-parent-value")
	}
	t.Setenv("ASTERISK_PJSIP_CONFIG_DIR", "")
	t.Setenv("FALEPACO_AUDIOSOCKET_ENABLED", "false")
	p := filepath.Join(t.TempDir(), ".env")
	var b strings.Builder
	for _, k := range config.RequiredLocalRuntimeVariables {
		v := "fixture"
		if k == "OUTBOUND_CALL_DESTINATION_ALLOWLIST" {
			v = "+55 (67) 98134-0687"
		}
		b.WriteString(k + "='" + v + "'\n")
	}
	b.WriteString("OWNER_LOGIN_PASSWORD_HASH='$2a$literal-secret'\n")
	t.Setenv("OWNER_LOGIN_PASSWORD_HASH", "")
	os.WriteFile(p, []byte(b.String()), 0600)
	if e := config.LoadLocalEnv(p); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := config.ValidateLocalRuntime(&out); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), "literal-secret") || strings.Contains(out.String(), "fixture") {
		t.Fatal("value leaked")
	}
	c, e := config.Load()
	if e != nil {
		t.Fatal(e)
	}
	a, e := callservice.NewAllowlist(c.OutboundCallAllowlist)
	if e != nil || !a.Allows("+5567981340687") {
		t.Fatal("restart lost policy")
	}
	if os.Getenv("OWNER_LOGIN_PASSWORD_HASH") != "$2a$literal-secret" {
		t.Fatal("secret interpolated")
	}
}
func TestLocalLaunchMissingFileKeyDoesNotInheritRequiredValue(t *testing.T) {
	for _, k := range config.RequiredLocalRuntimeVariables {
		t.Setenv(k, "parent-secret")
	}
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("OUTBOUND_CALL_DESTINATION_ALLOWLIST=+5567981340687\n"), 0600)
	if e := config.LoadLocalEnv(p); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := config.ValidateLocalRuntime(&out); e == nil {
		t.Fatal("inherited missing required keys")
	}
	if strings.Contains(out.String(), "parent-secret") {
		t.Fatal("secret leaked")
	}
}
func TestLocalEnvRejectsUnsafeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("GEMINI_API_KEY=secret\n"), 0644)
	if config.LoadLocalEnv(p) == nil {
		t.Fatal("unsafe permissions accepted")
	}
	os.Chmod(p, 0600)
	link := p + "-link"
	os.Symlink(p, link)
	if config.LoadLocalEnv(link) == nil {
		t.Fatal("symlink accepted")
	}
}
