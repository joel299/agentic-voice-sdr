package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	instances   []Instance
	validateErr error
	statusByID  map[string]Instance
	statusErr   error
	lastBaseURL string
}

type fakeBinding struct{ active Instance }

func (b *fakeBinding) SetActiveWhatsAppInstance(_ context.Context, instance Instance) error {
	b.active = instance
	return nil
}

func (b *fakeBinding) ClearActiveWhatsAppInstance(context.Context) error {
	b.active = Instance{}
	return nil
}

func (f *fakeProvider) ValidateConnection(context.Context, string, string) error {
	return f.validateErr
}
func (f *fakeProvider) ListInstances(_ context.Context, baseURL, _ string) ([]Instance, error) {
	f.lastBaseURL = baseURL
	return append([]Instance(nil), f.instances...), nil
}
func (f *fakeProvider) GetInstanceStatus(_ context.Context, baseURL, _ string, id string) (Instance, error) {
	f.lastBaseURL = baseURL
	if f.statusErr != nil {
		return Instance{}, f.statusErr
	}
	instance, ok := f.statusByID[id]
	if !ok {
		return Instance{}, ErrInstanceNotFound
	}
	return instance, nil
}
func (f *fakeProvider) SendMessage(context.Context, string, string, string, string, string) error {
	return nil
}

func configuredService(provider *fakeProvider) *Service {
	return NewService(NewRegistry(map[string]WhatsAppProvider{"test": provider}), nil)
}

func TestConfigureAndGetNeverExposeCredential(t *testing.T) {
	svc := configuredService(&fakeProvider{instances: []Instance{{ID: "wa-1", Status: StatusConnected}}})
	got, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "super-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.CredentialConfigured || got.Provider != "test" {
		t.Fatalf("unexpected safe config: %+v", got)
	}
	if got.ActiveInstanceID != "" {
		t.Fatal("configuration should not select an instance")
	}
	if _, encoded := any(got).(string); encoded {
		t.Fatal("safe config must not be a string secret")
	}
}

func TestConfigureRejectsInvalidURLAndCredential(t *testing.T) {
	svc := configuredService(&fakeProvider{})
	for _, input := range []ConfigInput{
		{Provider: "test", BaseURL: "ftp://provider.example.test", Credential: "x"},
		{Provider: "test", BaseURL: "http://127.0.0.1:8080", Credential: "x"},
		{Provider: "test", BaseURL: "https://provider.example.test", Credential: ""},
	} {
		if _, err := svc.Configure(context.Background(), input); err == nil {
			t.Fatalf("expected invalid input rejection: %+v", input)
		}
	}
}

func TestDiscoverySelectionAndStatusRefresh(t *testing.T) {
	provider := &fakeProvider{
		instances:  []Instance{{ID: "wa-1", Name: "Primary", Phone: "+5511999999999", Status: StatusConnected}, {ID: "wa-2", Status: StatusDisconnected}},
		statusByID: map[string]Instance{"wa-1": {ID: "wa-1", Phone: "+5511999999999", Status: StatusReady}, "wa-2": {ID: "wa-2", Status: StatusDisconnected}},
	}
	svc := configuredService(provider)
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"}); err != nil {
		t.Fatal(err)
	}
	instances, err := svc.Discover(context.Background())
	if err != nil || len(instances) != 2 || instances[0].ID != "wa-1" {
		t.Fatalf("discovery = %+v, err=%v", instances, err)
	}
	if _, err := svc.SelectInstance(context.Background(), "missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("missing selection err=%v", err)
	}
	if _, err := svc.SelectInstance(context.Background(), "wa-2"); !errors.Is(err, ErrInstanceNotReady) {
		t.Fatalf("disconnected selection err=%v", err)
	}
	selected, err := svc.SelectInstance(context.Background(), "wa-1")
	if err != nil || selected.ActiveInstanceID != "wa-1" {
		t.Fatalf("selection = %+v, err=%v", selected, err)
	}
	got, err := svc.Get(context.Background())
	if err != nil || got.ProviderStatus != StatusReady || got.ActiveInstancePhone != "+5511999999999" {
		t.Fatalf("refresh = %+v, err=%v", got, err)
	}
}

func TestSelectionBindsRuntimeInstance(t *testing.T) {
	provider := &fakeProvider{
		instances:  []Instance{{ID: "wa-1", Status: StatusConnected}},
		statusByID: map[string]Instance{"wa-1": {ID: "wa-1", Status: StatusReady}},
	}
	binding := &fakeBinding{}
	svc := NewService(NewRegistry(map[string]WhatsAppProvider{"test": provider}), binding)
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SelectInstance(context.Background(), "wa-1"); err != nil {
		t.Fatal(err)
	}
	if binding.active.ID != "wa-1" || binding.active.Status != StatusReady {
		t.Fatalf("binding=%+v", binding.active)
	}
}

func TestTestConnectionRejectsDisconnectedAndAuthFailure(t *testing.T) {
	provider := &fakeProvider{validateErr: errors.New("invalid auth"), instances: []Instance{{ID: "wa-1", Status: StatusConnected}}, statusByID: map[string]Instance{"wa-1": {ID: "wa-1", Status: StatusConnected}}}
	svc := configuredService(provider)
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"}); err == nil {
		t.Fatal("expected auth failure")
	}
	provider.validateErr = nil
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Test(context.Background()); err == nil {
		t.Fatal("expected no active instance failure")
	}
	if _, err := svc.SelectInstance(context.Background(), "wa-1"); err != nil {
		t.Fatal(err)
	}
	provider.statusByID["wa-1"] = Instance{ID: "wa-1", Status: StatusDisconnected}
	if _, err := svc.Test(context.Background()); !errors.Is(err, ErrInstanceNotReady) {
		t.Fatalf("test err=%v", err)
	}
}

func TestReconfigureClearsRuntimeBindingAndUsesBaseURL(t *testing.T) {
	provider := &fakeProvider{instances: []Instance{{ID: "wa-1", Status: StatusConnected}}, statusByID: map[string]Instance{"wa-1": {ID: "wa-1", Status: StatusReady}}}
	binding := &fakeBinding{}
	svc := NewService(NewRegistry(map[string]WhatsAppProvider{"test": provider}), binding)
	first := ConfigInput{Provider: "test", BaseURL: "https://first.example.test/api", Credential: "x"}
	if _, err := svc.Configure(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SelectInstance(context.Background(), "wa-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://second.example.test/api", Credential: "y"}); err != nil {
		t.Fatal(err)
	}
	if binding.active.ID != "" {
		t.Fatalf("stale runtime binding: %+v", binding.active)
	}
	if _, err := svc.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.lastBaseURL != "https://second.example.test/api" {
		t.Fatalf("base URL not forwarded: %q", provider.lastBaseURL)
	}
}

func TestTestPreservesProviderErrorAndSparseStatus(t *testing.T) {
	provider := &fakeProvider{instances: []Instance{{ID: "wa-1", Phone: "+5511", Status: StatusConnected}}, statusByID: map[string]Instance{"wa-1": {ID: "wa-1", Phone: "+5511", Status: StatusReady}}}
	svc := configuredService(provider)
	if _, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SelectInstance(context.Background(), "wa-1"); err != nil {
		t.Fatal(err)
	}
	provider.statusByID["wa-1"] = Instance{ID: "wa-1", Status: StatusReady}
	got, err := svc.Test(context.Background())
	if err != nil || got.ActiveInstancePhone != "+5511" {
		t.Fatalf("sparse status merge=%+v err=%v", got, err)
	}
	provider.statusErr = errors.New("provider leaked-value")
	if _, err := svc.Test(context.Background()); !errors.Is(err, ErrProviderOperation) {
		t.Fatalf("provider error=%v", err)
	}
}

func TestSafeConfigTimestamp(t *testing.T) {
	svc := configuredService(&fakeProvider{})
	before := time.Now()
	got, err := svc.Configure(context.Background(), ConfigInput{Provider: "test", BaseURL: "https://provider.example.test", Credential: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.LastVerifiedAt.Before(before) {
		t.Fatalf("verification timestamp not set: %v", got.LastVerifiedAt)
	}
}
