package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndReadiness(t *testing.T) {
	router := NewRouter()
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		want := http.StatusOK
		if path == "/readyz" {
			want = http.StatusServiceUnavailable
		}
		if res.Code != want {
			t.Fatalf("GET %s status = %d, want %d", path, res.Code, want)
		}
		if got := res.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("GET %s Content-Type = %q, want application/json", path, got)
		}
	}
}

func TestUnknownRoute(t *testing.T) {
	res := httptest.NewRecorder()
	NewRouter().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want %d", res.Code, http.StatusNotFound)
	}
}
