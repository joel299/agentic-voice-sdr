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

	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type fakeFalePacoSIPService struct {
	config FalePacoSIPConfig
	check  FalePacoSIPValidation
	err    error
}

func (f *fakeFalePacoSIPService) Get(context.Context) (FalePacoSIPConfig, error) {
	return f.config, f.err
}
func (f *fakeFalePacoSIPService) ValidateRegistration(context.Context) (FalePacoSIPValidation, error) {
	return f.check, f.err
}

func falePacoTestRouter(service FalePacoSIPProfileService, token string) http.Handler {
	authorizer, _ := NewStaticBearerAuthorizer(token)
	return NewRouterWithCalibration(whatsapp.NewService(nil, nil), unavailableSIPConfigurator{}, nil, nil, authorizer, CalibrationServices{FalePacoSIP: service})
}

func sipRequest(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

func TestFalePacoSIPGetIsCanonicalSafeAndOwnerOnly(t *testing.T) {
	service := &fakeFalePacoSIPService{config: CanonicalFalePacoSIPForRuntime(true)}
	h := falePacoTestRouter(service, "owner-token")
	if got := sipRequest(t, h, http.MethodGet, "/v1/config/sip-trunk", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d", got.Code)
	}
	res := sipRequest(t, h, http.MethodGet, "/v1/config/sip-trunk", "", "owner-token")
	var got FalePacoSIPConfig
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	want := CanonicalFalePacoSIPForRuntime(true)
	if got != want || strings.Contains(res.Body.String(), "secret") || strings.Contains(res.Body.String(), "password\"") {
		t.Fatalf("unexpected safe profile: %#v body=%s", got, res.Body)
	}
}

func TestFalePacoSIPPutCannotMutateLocalCredential(t *testing.T) {
	service := &fakeFalePacoSIPService{config: CanonicalFalePacoSIPForRuntime(true)}
	h := falePacoTestRouter(service, "owner-token")
	path := filepath.Join(t.TempDir(), "accounts")
	fixture := []byte("<sip:100@98034.falepaco.com.br:5060;transport=tcp>;auth_user=100;auth_pass=fixture-secret;outbound=\\\"sip:98034.falepaco.com.br:5060;transport=tcp\\\";regint=600\\n")
	if err := os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	res := sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"attacker-value"}`, "owner-token")
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "credential_managed_locally") || strings.Contains(res.Body.String(), "attacker-value") {
		t.Fatalf("mutation was not safely rejected: status=%d body=%s", res.Code, res.Body)
	}
	after, err := os.Stat(path)
	contents, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || string(contents) != string(fixture) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("PUT changed fixture account bytes or mtime")
	}
	res = sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"x"}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated secret write status=%d", res.Code)
	}
}

func TestFalePacoRegistrationValidationReturnsOnlySafeFacts(t *testing.T) {
	service := &fakeFalePacoSIPService{check: FalePacoSIPValidation{OK: true, PasswordConfigured: true, CredentialPresent: true, DomainMatch: true, UsernameMatch: true, RealmMatch: true, Transport: "tcp", RegistrationRequired: true, RegistrationState: "REGISTERED"}}
	h := falePacoTestRouter(service, "owner-token")
	res := sipRequest(t, h, http.MethodPost, "/v1/config/sip-trunk/validate", "", "owner-token")
	var got FalePacoSIPValidation
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil || got != service.check {
		t.Fatalf("status=%d result=%#v body=%s", res.Code, got, res.Body)
	}
	if strings.Contains(res.Body.String(), "digest") || strings.Contains(res.Body.String(), "fixture-sip-password") {
		t.Fatalf("validation leaked sensitive detail: %s", res.Body)
	}
}

func TestFalePacoSIPInvalidSecretAndRuntimeErrorsAreSanitized(t *testing.T) {
	service := &fakeFalePacoSIPService{err: errors.New("fixture provider error")}
	h := falePacoTestRouter(service, "owner-token")
	res := sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `malformed body with secret=never-log`, "owner-token")
	if res.Code != http.StatusConflict || strings.Contains(res.Body.String(), "never-log") || strings.Contains(res.Body.String(), "fixture provider error") {
		t.Fatalf("disabled mutation response was not sanitized: status=%d body=%s", res.Code, res.Body)
	}
}
