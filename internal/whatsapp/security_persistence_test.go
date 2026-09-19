package whatsapp

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestConfigStoreRoundTripDoesNotPersistCredential(t *testing.T) {
	store := NewMemoryConfigStore()
	metadata := ConfigMetadata{Provider: "test", BaseURL: "https://provider.example", ActiveInstanceID: "wa-1", ActiveInstancePhone: "+5511", ProviderStatus: StatusReady}
	if err := store.Save(context.Background(), metadata); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != metadata {
		t.Fatalf("metadata mismatch: %#v", got)
	}
}

func TestFileConfigStoreRoundTrip(t *testing.T) {
	store := &FileConfigStore{Path: t.TempDir() + "/whatsapp.json"}
	want := ConfigMetadata{Provider: "test", BaseURL: "https://provider.example", ActiveInstanceID: "wa-1"}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("metadata mismatch: %#v", got)
	}
}

func TestHTTPProviderRejectsPrivateRedirectAndDialResolution(t *testing.T) {
	p := NewHTTPProvider("instances", func(base, id string) string { return base + "/status/" + id })
	p.Resolver = func(_ context.Context, host string) ([]net.IP, error) {
		if host == "private.example" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	if _, err := p.ListInstances(context.Background(), "https://private.example", "secret"); err == nil {
		t.Fatal("private resolved address accepted")
	}
	p.Client = &http.Client{Transport: providerRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://private.example/private"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := p.ListInstances(context.Background(), "https://public.example", "secret"); err == nil {
		t.Fatal("redirect to private address accepted")
	}
}
