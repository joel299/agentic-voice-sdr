package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/ownerauth"
)

func TestOwnerInteractiveLoginSessionAndLogout(t *testing.T) {
	hash, err := ownerauth.HashPassword("test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	api := NewOwnerAuthService("owner", hash, 8*time.Hour, nil)
	router := newRouterWithCalibration(nil, nil, nil, api, CalibrationServices{})
	login := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(`{"username":"owner","password":"test-only-password"}`))
	login.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, login)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Token   string `json:"access_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	rawToken, decodeErr := base64.RawURLEncoding.DecodeString(result.Token)
	if decodeErr != nil || len(rawToken) < 32 || result.Type != "Bearer" || result.Expires != 28800 {
		t.Fatalf("unexpected login response metadata")
	}
	if strings.Contains(string(response.Body.Bytes()), hash) {
		t.Fatal("password hash returned")
	}
	if len(api.sessions) != 1 {
		t.Fatalf("raw token storage/session count mismatch: %d", len(api.sessions))
	}
	for digest := range api.sessions {
		if digest != sha256.Sum256([]byte(result.Token)) {
			t.Fatal("session map did not store only the token SHA-256 digest")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"session_type":"interactive"`) {
		t.Fatalf("session status=%d body=%s", response.Code, response.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("logout status=%d", response.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", response.Code)
	}
}

func TestOwnerLoginGenericFailuresAndRateLimit(t *testing.T) {
	hash, err := ownerauth.HashPassword("test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	api := NewOwnerAuthService("owner", hash, time.Hour, nil)
	for _, body := range []string{`{"username":"wrong","password":"test-only-password"}`, `{"username":"owner","password":"wrong"}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(body))
		res := httptest.NewRecorder()
		api.login(res, req)
		if res.Code != http.StatusUnauthorized || res.Body.String() != `{"error":"invalid credentials"}`+"\n" {
			t.Fatalf("failure response differs: %d %s", res.Code, res.Body.String())
		}
	}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"owner","password":"wrong"}`))
		res := httptest.NewRecorder()
		api.login(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", i, res.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"owner","password":"test-only-password"}`))
	res := httptest.NewRecorder()
	api.login(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limit status=%d", res.Code)
	}
}

func TestOwnerSessionExpiryAndLegacyBearerCompatibility(t *testing.T) {
	hash, err := ownerauth.HashPassword("test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewStaticBearerAuthorizer("test-legacy-token")
	if err != nil {
		t.Fatal(err)
	}
	api := NewOwnerAuthService("owner", hash, time.Nanosecond, legacy)
	login := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"owner","password":"test-only-password"}`))
	res := httptest.NewRecorder()
	api.login(res, login)
	if res.Code != http.StatusOK {
		t.Fatalf("login status=%d", res.Code)
	}
	var payload struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+payload.Token)
	if api.Authorize(req) {
		t.Fatal("expired token remained authorized")
	}
	req.Header.Set("Authorization", "Bearer test-legacy-token")
	if !api.Authorize(req) {
		t.Fatal("legacy owner token stopped authorizing protected routes")
	}
	router := NewRouterWithServicesAndCalls(nil, unavailableSIPConfigurator{}, &fakeOutboundCallService{}, api)
	protected := httptest.NewRequest(http.MethodGet, "/v1/calls/call-123", nil)
	protected.Header.Set("Authorization", "Bearer test-legacy-token")
	protectedResponse := httptest.NewRecorder()
	router.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("legacy protected route status=%d", protectedResponse.Code)
	}
	username, kind, _, ok := api.authenticate(req)
	if !ok || username != "owner" || kind != "legacy" {
		t.Fatal("legacy session metadata missing")
	}
}

func TestOwnerLoginOpenAPIAuthorizationContract(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(contents)
	for _, part := range []string{"/v1/auth/login:", "/v1/auth/session:", "/v1/auth/logout:", "OwnerLoginRequest:", "OwnerLoginResponse:", "tags: [Authentication]", "example: { username: owner, password: YOUR_PASSWORD }", "'429':"} {
		if !strings.Contains(spec, part) {
			t.Errorf("OpenAPI auth contract lacks %q", part)
		}
	}
	loginStart := strings.Index(spec, "  /v1/auth/login:\n")
	sessionStart := strings.Index(spec, "  /v1/auth/session:\n")
	logoutStart := strings.Index(spec, "  /v1/auth/logout:\n")
	callsStart := strings.Index(spec, "  /v1/calls:\n")
	if loginStart < 0 || sessionStart < 0 || logoutStart < 0 || callsStart < 0 {
		t.Fatal("auth operations missing")
	}
	login := spec[loginStart:sessionStart]
	session := spec[sessionStart:logoutStart]
	logout := spec[logoutStart:callsStart]
	if strings.Contains(login, "BearerAuth") || !strings.Contains(session, "BearerAuth") || !strings.Contains(logout, "BearerAuth") {
		t.Fatal("login/session/logout security requirement mismatch")
	}
}
