package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type fakeFalePacoSIPService struct {
	password string
	config   FalePacoSIPConfig
	check    FalePacoSIPValidation
	err      error
}

func (f *fakeFalePacoSIPService) Get(context.Context) (FalePacoSIPConfig, error) {
	return f.config, f.err
}
func (f *fakeFalePacoSIPService) ConfigurePassword(_ context.Context, password string) (FalePacoSIPConfig, error) {
	if f.err != nil {
		return FalePacoSIPConfig{}, f.err
	}
	f.password = password
	f.config.PasswordConfigured = true
	return f.config, nil
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

func TestFalePacoSIPPutAcceptsOnlyWriteOnlySecret(t *testing.T) {
	service := &fakeFalePacoSIPService{config: CanonicalFalePacoSIPForRuntime(false)}
	h := falePacoTestRouter(service, "owner-token")
	res := sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"fixture-sip-password"}`, "owner-token")
	if res.Code != http.StatusOK || service.password != "fixture-sip-password" || strings.Contains(res.Body.String(), "fixture-sip-password") {
		t.Fatalf("status=%d password received=%v body=%s", res.Code, service.password != "", res.Body)
	}
	res = sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"x","host":"attacker.example"}`, "owner-token")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("generic SIP fields must be rejected, status=%d body=%s", res.Code, res.Body)
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
	service := &fakeFalePacoSIPService{err: ErrInvalidSIPSecret}
	h := falePacoTestRouter(service, "owner-token")
	res := sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"secret"}`, "owner-token")
	if res.Code != http.StatusBadRequest || !errors.Is(service.err, ErrInvalidSIPSecret) || strings.Contains(res.Body.String(), "fixture-secret") {
		t.Fatalf("invalid secret error not sanitized: status=%d body=%s", res.Code, res.Body)
	}
	service.err = errors.New("private process stderr secret=leak")
	res = sipRequest(t, h, http.MethodPut, "/v1/config/sip-trunk", `{"secret":"secret"}`, "owner-token")
	if res.Code != http.StatusBadGateway || strings.Contains(res.Body.String(), "private") || strings.Contains(res.Body.String(), "leak") {
		t.Fatalf("runtime error not sanitized: status=%d body=%s", res.Code, res.Body)
	}
}
