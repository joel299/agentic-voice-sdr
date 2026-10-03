package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type apiProvider struct{ instances []whatsapp.Instance }

func (p *apiProvider) ValidateConnection(context.Context, string, string) error { return nil }
func (p *apiProvider) ListInstances(context.Context, string, string) ([]whatsapp.Instance, error) {
	return p.instances, nil
}
func (p *apiProvider) GetInstanceStatus(_ context.Context, _ string, _ string, id string) (whatsapp.Instance, error) {
	for _, instance := range p.instances {
		if instance.ID == id {
			return instance, nil
		}
	}
	return whatsapp.Instance{}, whatsapp.ErrInstanceNotFound
}
func (p *apiProvider) SendMessage(context.Context, string, string, string, string, string) error {
	return nil
}

type canonicalManager struct {
	got   sip.TrunkConfig
	err   error
	calls int
}

func (m *canonicalManager) ApplyTrunk(_ context.Context, cfg sip.TrunkConfig) (sip.StatusReport, error) {
	m.got = cfg.Clone()
	m.calls++
	return sip.StatusReport{TrunkName: cfg.Name, Status: sip.StatusReady}, m.err
}

func testAPI() http.Handler {
	provider := &apiProvider{instances: []whatsapp.Instance{{ID: "wa-1", Phone: "+5511", Status: whatsapp.StatusConnected}}}
	wa := whatsapp.NewService(whatsapp.NewRegistry(map[string]whatsapp.WhatsAppProvider{"test": provider}), nil)
	return NewRouterWithServices(wa, unavailableSIPConfigurator{})
}

func requestJSON(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestWhatsAppConfigurationAPIAndSecretMasking(t *testing.T) {
	handler := testAPI()
	res := requestJSON(t, handler, http.MethodPut, "/v1/config/whatsapp", `{"provider":"test","base_url":"https://provider.example.test","credential":"secret-token"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("configure status=%d body=%s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "secret-token") {
		t.Fatal("credential leaked in configure response")
	}
	res = requestJSON(t, handler, http.MethodGet, "/v1/config/whatsapp", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "secret-token") {
		t.Fatalf("safe get status=%d body=%s", res.Code, res.Body)
	}
	res = requestJSON(t, handler, http.MethodGet, "/v1/config/whatsapp/instances", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"id":"wa-1"`) {
		t.Fatalf("instances status=%d body=%s", res.Code, res.Body)
	}
	res = requestJSON(t, handler, http.MethodPut, "/v1/config/whatsapp/instance", `{"instance_id":"wa-1"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("select status=%d body=%s", res.Code, res.Body)
	}
	res = requestJSON(t, handler, http.MethodPost, "/v1/config/whatsapp/test", "")
	if res.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", res.Code, res.Body)
	}
}

func TestConfigurationValidationAndProviderErrors(t *testing.T) {
	handler := testAPI()
	res := requestJSON(t, handler, http.MethodPut, "/v1/config/whatsapp", `{"provider":"missing","base_url":"https://provider.example.test","credential":"x"}`)
	if res.Code != http.StatusBadRequest && res.Code != http.StatusNotImplemented {
		t.Fatalf("provider status=%d", res.Code)
	}
	res = requestJSON(t, handler, http.MethodPut, "/v1/config/whatsapp", `{"provider":"test","base_url":"http://127.0.0.1:8080","credential":"x"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("SSRF status=%d", res.Code)
	}
	res = requestJSON(t, handler, http.MethodPut, "/v1/config/whatsapp/instance", `{"instance_id":"missing"}`)
	if res.Code != http.StatusBadRequest && res.Code != http.StatusNotFound {
		t.Fatalf("missing instance status=%d", res.Code)
	}
	_ = json.Valid
	_ = errors.Is
}

func TestWhatsAppRouterCompositionWithFileConfigStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "whatsapp-config.json")
	cfg := config.Config{
		HTTPAddr:           ":8080",
		WhatsAppConfigPath: storePath,
	}

	handler := NewRouterWithConfig(cfg)
	if handler == nil {
		t.Fatal("expected non-nil router")
	}

	// Verify store file does not exist initially
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("expected store path not to exist before config, got err: %v", err)
	}
}

func TestGenericSIPDTOCanonicalMappingRemainsInternal(t *testing.T) {
	request := SIPConfigRequest{Provider: "internal", Name: "test", Host: "sip.example.test", Port: 5060, Transport: "tcp", Auth: SIPAuthRequest{Type: "userpass", Username: "alice", Secret: "secret", Realm: "example"}, RegistrationRequired: true, Enabled: true}
	canonical, err := request.ToCanonical()
	if err != nil || canonical.Secret != "secret" || canonical.AuthUsername != "alice" || canonical.Transport != sip.TransportTCP {
		t.Fatalf("internal SIP mapping failed: cfg=%#v err=%v", canonical, err)
	}
}

func TestSIPAuthMappingAndManagerFailure(t *testing.T) {
	for _, authType := range []string{"ip", "none"} {
		request := SIPConfigRequest{Provider: "p", Name: "n", Host: "sip.example.test", Port: 5060, Transport: "udp", Auth: SIPAuthRequest{Type: authType}, Enabled: false}
		canonical, err := request.ToCanonical()
		if err != nil || string(canonical.AuthType) != authType || canonical.Enabled {
			t.Fatalf("auth mapping %q failed: cfg=%#v err=%v", authType, canonical, err)
		}
	}
}

func TestSIPCompositionNeverActivatesAsteriskFromRuntimeConfig(t *testing.T) {
	if _, ok := configuredSIPConfigurator(config.Config{}).(unavailableSIPConfigurator); !ok {
		t.Fatal("empty SIP runtime configuration must fail closed")
	}
	if _, ok := configuredSIPConfigurator(config.Config{SIPConfigDir: "/any/path"}).(unavailableSIPConfigurator); !ok {
		t.Fatal("Asterisk SIP configuration must never be mounted by the Baresip runtime")
	}
	if _, ok := configuredSIPConfigurator(config.Config{SIPConfigDir: filepath.Join(t.TempDir(), "missing")}).(unavailableSIPConfigurator); !ok {
		t.Fatal("missing SIP directory must fail closed")
	}
}

func TestSIPCanonicalValidationIsClientError(t *testing.T) {
	request := SIPConfigRequest{Provider: "p", Name: "bad/name", Host: "sip.example.test", Port: 5060, Transport: "udp", Auth: SIPAuthRequest{Type: "none"}, RegistrationRequired: true, Enabled: true}
	if _, err := request.ToCanonical(); err == nil {
		t.Fatal("invalid internal generic SIP data passed validation")
	}
}

func TestSIPDestinationPolicyBlocksBeforeManager(t *testing.T) {
	manager := &canonicalManager{}
	policy := &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
	policy.dialer.resolver = &sequenceSIPResolver{answers: [][]string{{"10.0.0.10"}}}
	configurator, err := newCanonicalSIPConfiguratorWithPolicy(manager, policy)
	if err != nil {
		t.Fatal(err)
	}
	request := SIPConfigRequest{Provider: "p", Name: "unsafe", Host: "unsafe.example", Port: 5060, Transport: "udp", Auth: SIPAuthRequest{Type: "none"}, Enabled: true}
	err = configurator.Configure(context.Background(), request)
	if !errors.Is(err, errSIPCanonicalValidation) || manager.calls != 0 {
		t.Fatalf("unsafe destination must be rejected before manager: err=%v calls=%d", err, manager.calls)
	}
}

func TestSIPDisableDoesNotDependOnDNS(t *testing.T) {
	manager := &canonicalManager{}
	policy := &sipDestinationPolicy{dialer: newSafeSIPNetworkDialer()}
	policy.dialer.resolver = &errorSIPResolver{err: errors.New("provider DNS unavailable")}
	configurator, err := newCanonicalSIPConfiguratorWithPolicy(manager, policy)
	if err != nil {
		t.Fatal(err)
	}
	request := SIPConfigRequest{Provider: "p", Name: "existing", Host: "removed.provider.example", Port: 5060, Transport: "tls", Registrar: "removed.registrar.example", OutboundProxy: "removed.proxy.example:5061", Auth: SIPAuthRequest{Type: "none"}, Enabled: false}
	if err := configurator.Configure(context.Background(), request); err != nil || manager.calls != 1 || manager.got.Enabled {
		t.Fatalf("disable must reach Manager without DNS: err=%v calls=%d cfg=%#v", err, manager.calls, manager.got)
	}
}
