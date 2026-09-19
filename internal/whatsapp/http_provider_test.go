package whatsapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPProviderNormalizesBoundedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/instances" {
			_, _ = w.Write([]byte(`{"data":[{"uuid":"i-1","instance_name":"Main","phone_number":"+5511","state":"connected"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"i-1","status":"READY","phone":"+5511"}`))
	}))
	defer server.Close()
	provider := NewHTTPProvider(server.URL+"/instances", func(string) string { return server.URL + "/status" })
	instances, err := provider.ListInstances(context.Background(), "secret")
	if err != nil || len(instances) != 1 || instances[0].ID != "i-1" || instances[0].Status != "CONNECTED" {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
	status, err := provider.GetInstanceStatus(context.Background(), "secret", "i-1")
	if err != nil || status.Status != "READY" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestHTTPProviderDoesNotExposeAuthErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "secret-token", http.StatusUnauthorized) }))
	defer server.Close()
	provider := NewHTTPProvider(server.URL, nil)
	if err := provider.ValidateConnection(context.Background(), "secret-token"); err == nil || err.Error() != "provider authentication failed" {
		t.Fatalf("err=%v", err)
	}
}
