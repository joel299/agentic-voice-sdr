package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWTValidAndTampered(t *testing.T) {
	now := time.Now()
	c := tokenClaims{Sub: "owner", Iat: now.Unix(), Exp: now.Add(15 * time.Minute).Unix(), Iss: issuer, Aud: audience, JTI: "jti"}
	tok, err := signToken(c, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := verifyToken(tok, map[string]string{"SDR_TEST_JWT_SECRET": "test-secret"})
	if !ok || got.Sub != "owner" {
		t.Fatalf("valid token rejected: %#v %v", got, ok)
	}
	parts := strings.Split(tok, ".")
	parts[1] = "x" + parts[1][1:]
	tampered := strings.Join(parts, ".")
	if _, ok := verifyToken(tampered, map[string]string{"SDR_TEST_JWT_SECRET": "test-secret"}); ok {
		t.Fatal("tampered token accepted")
	}
}

func TestJWTExpired(t *testing.T) {
	now := time.Now()
	tok, err := signToken(tokenClaims{Sub: "owner", Iat: now.Add(-time.Hour).Unix(), Exp: now.Add(-time.Minute).Unix(), Iss: issuer, Aud: audience, JTI: "jti"}, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verifyToken(tok, map[string]string{"SDR_TEST_JWT_SECRET": "test-secret"}); ok {
		t.Fatal("expired token accepted")
	}
}

func TestLoginMissingFieldsIs400(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"username":"owner"}`))
	rec := httptest.NewRecorder()
	(&server{}).login(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
