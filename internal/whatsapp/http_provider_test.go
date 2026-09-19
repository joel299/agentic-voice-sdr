package whatsapp

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type providerRoundTripper func(*http.Request) (*http.Response, error)

func (f providerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publicResolver(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestHTTPProviderNormalizesBoundedContract(t *testing.T) {
	provider := NewHTTPProvider("/instances", func(baseURL, _ string) string { return baseURL + "/status" })
	provider.Resolver = publicResolver
	provider.Client = &http.Client{Transport: providerRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization header missing")
		}
		if r.URL.Path == "/instances" {
			return response(http.StatusOK, `{"data":[{"uuid":"i-1","instance_name":"Main","phone_number":"+5511","state":"connected"}]}`), nil
		}
		return response(http.StatusOK, `{"id":"i-1","status":"READY","phone":"+5511"}`), nil
	})}
	instances, err := provider.ListInstances(context.Background(), "https://provider.example", "secret")
	if err != nil || len(instances) != 1 || instances[0].ID != "i-1" || instances[0].Status != "CONNECTED" {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
	status, err := provider.GetInstanceStatus(context.Background(), "https://provider.example", "secret", "i-1")
	if err != nil || status.Status != "READY" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestHTTPProviderDoesNotExposeAuthErrors(t *testing.T) {
	provider := NewHTTPProvider("/", nil)
	provider.Resolver = publicResolver
	provider.Client = &http.Client{Transport: providerRoundTripper(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "secret-token"), nil
	})}
	if err := provider.ValidateConnection(context.Background(), "https://provider.example", "secret-token"); err == nil || err.Error() != "provider authentication failed" {
		t.Fatalf("err=%v", err)
	}
}
