package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPProvider is an adapter boundary. Endpoint paths are injected by the
// provider-specific adapter; this package does not guess an external contract.
type HTTPProvider struct {
	Client            *http.Client
	ListInstancesPath string
	StatusPath        func(baseURL, instanceID string) string
	Resolver          func(context.Context, string) ([]net.IP, error)
}

func NewHTTPProvider(listPath string, statusPath func(string, string) string) *HTTPProvider {
	return &HTTPProvider{Client: &http.Client{Timeout: 5 * time.Second}, ListInstancesPath: listPath, StatusPath: statusPath}
}
func (p *HTTPProvider) ValidateConnection(ctx context.Context, baseURL, credential string) error {
	_, err := p.request(ctx, joinEndpoint(baseURL, p.ListInstancesPath), credential)
	return err
}
func (p *HTTPProvider) ListInstances(ctx context.Context, baseURL, credential string) ([]Instance, error) {
	body, err := p.request(ctx, joinEndpoint(baseURL, p.ListInstancesPath), credential)
	if err != nil {
		return nil, err
	}
	return normalizeInstances(body)
}
func (p *HTTPProvider) GetInstanceStatus(ctx context.Context, baseURL, credential, instanceID string) (Instance, error) {
	if p.StatusPath == nil {
		return Instance{}, errors.New("provider status endpoint is unavailable")
	}
	body, err := p.request(ctx, p.StatusPath(baseURL, instanceID), credential)
	if err != nil {
		return Instance{}, err
	}
	var instance Instance
	if err := json.Unmarshal(body, &instance); err != nil {
		return Instance{}, errors.New("provider returned invalid instance status")
	}
	if instance.ID == "" {
		instance.ID = instanceID
	}
	instance.Status = strings.ToUpper(strings.TrimSpace(instance.Status))
	if instance.ID == "" {
		return Instance{}, ErrInstanceNotFound
	}
	return instance, nil
}
func (p *HTTPProvider) SendMessage(context.Context, string, string, string, string, string) error {
	return errors.New("provider send-message contract is not configured")
}
func joinEndpoint(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (p *HTTPProvider) request(ctx context.Context, endpoint, credential string) ([]byte, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("provider endpoint is not configured")
	}
	if err := p.validateEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid provider endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	client := p.safeClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("provider unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("provider response unreadable")
	}
	if len(body) >= 1<<20 {
		return nil, errors.New("provider response too large")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("provider authentication failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (p *HTTPProvider) resolver() func(context.Context, string) ([]net.IP, error) {
	if p.Resolver != nil {
		return p.Resolver
	}
	return func(_ context.Context, host string) ([]net.IP, error) { return net.LookupIP(host) }
}
func (p *HTTPProvider) validateEndpoint(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("invalid provider endpoint")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("provider endpoint host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if forbiddenIP(ip) {
			return errors.New("provider endpoint host is not allowed")
		}
		return nil
	}
	ips, err := p.resolver()(ctx, host)
	if err != nil {
		return errors.New("provider host resolution failed")
	}
	if len(ips) == 0 {
		return errors.New("provider host resolution failed")
	}
	for _, ip := range ips {
		if forbiddenIP(ip) {
			return errors.New("provider endpoint resolves to a private host")
		}
	}
	return nil
}
func (p *HTTPProvider) safeClient() *http.Client {
	base := p.Client
	if base == nil {
		base = &http.Client{Timeout: 5 * time.Second}
	}
	client := *base
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := p.validateEndpoint(req.Context(), req.URL.String()); err != nil {
			return err
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}

	var baseTransport *http.Transport
	if base.Transport == nil {
		if defTransport, ok := http.DefaultTransport.(*http.Transport); ok && defTransport != nil {
			baseTransport = defTransport.Clone()
		} else {
			baseTransport = &http.Transport{}
		}
	} else if t, ok := base.Transport.(*http.Transport); ok && t != nil {
		baseTransport = t.Clone()
	}

	if baseTransport != nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		baseTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := p.resolver()(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if forbiddenIP(ip) {
					return nil, errors.New("provider dial target is not allowed")
				}
			}
			if len(ips) == 0 {
				return nil, errors.New("provider dial target unavailable")
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		client.Transport = baseTransport
	}
	return &client
}
func normalizeInstances(body []byte) ([]Instance, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("provider returned invalid instance list")
	}
	items := raw
	if object, ok := raw.(map[string]any); ok {
		for _, key := range []string{"instances", "data", "results"} {
			if value, exists := object[key]; exists {
				items = value
				break
			}
		}
	}
	array, ok := items.([]any)
	if !ok {
		return nil, errors.New("provider instance list contract unavailable")
	}
	if len(array) > 1000 {
		return nil, errors.New("provider returned too many instances")
	}
	result := make([]Instance, 0, len(array))
	for _, item := range array {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("provider instance list contract unavailable")
		}
		result = append(result, Instance{ID: stringField(object, "id", "instance_id", "uuid"), Name: stringField(object, "name", "instance_name"), Phone: stringField(object, "phone", "phone_number", "number"), Status: strings.ToUpper(stringField(object, "status", "state"))})
	}
	return result, nil
}
func stringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			return value
		}
	}
	return ""
}
