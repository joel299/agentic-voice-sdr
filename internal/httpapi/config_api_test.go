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

type apiSIP struct{ configured bool }

func (s *apiSIP) Configure(context.Context, SIPConfigRequest) error { s.configured = true; return nil }

func testAPI() (http.Handler, *apiSIP) {
	provider := &apiProvider{instances: []whatsapp.Instance{{ID: "wa-1", Phone: "+5511", Status: whatsapp.StatusConnected}}}
	wa := whatsapp.NewService(whatsapp.NewRegistry(map[string]whatsapp.WhatsAppProvider{"test": provider}), nil)
	telephony := &apiSIP{}
	return NewRouterWithServices(wa, telephony), telephony
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
	handler, _ := testAPI()
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

func TestSIPConfigurationBoundaryMasksSecret(t *testing.T) {
	handler, boundary := testAPI()
	res := requestJSON(t, handler, http.MethodPut, "/v1/config/sip-trunk", `{"provider":"generic","name":"main","host":"sip.example.test","port":5060,"transport":"udp","auth":{"type":"userpass","username":"alice","secret":"secret"},"enabled":true}`)
	if res.Code != http.StatusOK || !boundary.configured {
		t.Fatalf("sip status=%d body=%s configured=%v", res.Code, res.Body, boundary.configured)
	}
	if strings.Contains(res.Body.String(), "secret") {
		t.Fatal("SIP secret leaked")
	}
}

func TestConfigurationValidationAndProviderErrors(t *testing.T) {
	handler, _ := testAPI()
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
