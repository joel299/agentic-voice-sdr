// Package n8nwhatsapp forwards the canonical WhatsApp send request to one
// configured n8n webhook. Downstream provider behavior belongs to n8n.
package n8nwhatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joel299/agentic-voice-sdr/internal/domain/tools"
)

const (
	CanonicalToolSendMessage = tools.ToolWhatsAppSendMessage
	DefaultTimeout           = 5 * time.Second
	MaxPhoneLength           = 64
	MaxMessageLength         = 4096
	maxResponseSize          = 64 << 10
)

var (
	ErrWhatsAppConfiguration   = errors.New("n8n WhatsApp configuration or request is invalid")
	ErrWhatsAppTimeout         = errors.New("n8n WhatsApp request timed out")
	ErrWhatsAppTransport       = errors.New("n8n WhatsApp transport failed")
	ErrWhatsAppRejected        = errors.New("n8n WhatsApp webhook rejected the request")
	ErrWhatsAppInvalidResponse = errors.New("n8n WhatsApp webhook returned an invalid response")
)

type Config struct {
	WebhookURL string
}

func ConfigFromEnv() (Config, error) {
	return validateConfig(Config{WebhookURL: os.Getenv("N8N_WHATSAPP_WEBHOOK_URL")})
}

type SendMessageRequest struct {
	Phone   string `json:"phone"`
	Message string `json:"message"`
}

type SendMessageResult struct {
	Accepted  bool   `json:"accepted"`
	MessageID string `json:"message_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type Client struct {
	webhookURL string
	httpClient *http.Client
	timeout    time.Duration
}

func New(config Config) (*Client, error) {
	return NewWithTimeout(config, DefaultTimeout)
}

func NewWithTimeout(config Config, timeout time.Duration) (*Client, error) {
	validated, err := validateConfig(config)
	if err != nil || timeout <= 0 {
		return nil, ErrWhatsAppConfiguration
	}
	return &Client{
		webhookURL: validated.WebhookURL,
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		timeout:    timeout,
	}, nil
}

func validateConfig(config Config) (Config, error) {
	config.WebhookURL = strings.TrimSpace(config.WebhookURL)
	u, err := url.Parse(config.WebhookURL)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Config{}, ErrWhatsAppConfiguration
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return Config{}, ErrWhatsAppConfiguration
	}
	return config, nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type webhookPayload struct {
	Tool    string `json:"tool"`
	Phone   string `json:"phone"`
	Message string `json:"message"`
}

type webhookAcknowledgement struct {
	Accepted  *bool  `json:"accepted"`
	MessageID string `json:"message_id"`
	RequestID string `json:"request_id"`
}

func (c *Client) SendMessage(ctx context.Context, request SendMessageRequest) (SendMessageResult, error) {
	if ctx == nil || !validRequest(request) {
		return SendMessageResult{}, ErrWhatsAppConfiguration
	}
	body, err := json.Marshal(webhookPayload{Tool: CanonicalToolSendMessage, Phone: request.Phone, Message: request.Message})
	if err != nil {
		return SendMessageResult{}, ErrWhatsAppConfiguration
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
	if err != nil {
		return SendMessageResult{}, ErrWhatsAppConfiguration
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SendMessageResult{}, classifyRequestError(ctx, requestCtx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return SendMessageResult{}, ErrWhatsAppRejected
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return SendMessageResult{}, classifyRequestError(ctx, requestCtx)
	}
	if len(responseBody) == 0 || len(responseBody) > maxResponseSize {
		return SendMessageResult{}, ErrWhatsAppInvalidResponse
	}
	var acknowledgement webhookAcknowledgement
	if err := json.Unmarshal(responseBody, &acknowledgement); err != nil || acknowledgement.Accepted == nil {
		return SendMessageResult{}, ErrWhatsAppInvalidResponse
	}
	if !*acknowledgement.Accepted {
		return SendMessageResult{}, ErrWhatsAppRejected
	}
	if !validReference(acknowledgement.MessageID) || !validReference(acknowledgement.RequestID) {
		return SendMessageResult{}, ErrWhatsAppInvalidResponse
	}
	return SendMessageResult{Accepted: true, MessageID: acknowledgement.MessageID, RequestID: acknowledgement.RequestID}, nil
}

func validRequest(request SendMessageRequest) bool {
	if strings.TrimSpace(request.Phone) == "" || len(request.Phone) > MaxPhoneLength || strings.ContainsAny(request.Phone, "\r\n") {
		return false
	}
	if strings.TrimSpace(request.Message) == "" || !utf8.ValidString(request.Message) || utf8.RuneCountInString(request.Message) > MaxMessageLength {
		return false
	}
	return true
}

func validReference(value string) bool {
	return len(value) <= 256 && !strings.ContainsAny(value, "\r\n")
}

func classifyRequestError(ctx, requestCtx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
		return ErrWhatsAppTimeout
	}
	return ErrWhatsAppTransport
}
