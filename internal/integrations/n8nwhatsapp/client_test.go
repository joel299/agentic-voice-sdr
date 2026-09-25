package n8nwhatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendMessagePostsOneTypedRequestToConfiguredWebhook(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/webhook/whatsapp" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept = %q", r.Header.Get("Accept"))
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
			return
		}
		var tool, phone, message string
		_ = json.Unmarshal(payload["tool"], &tool)
		_ = json.Unmarshal(payload["phone"], &phone)
		_ = json.Unmarshal(payload["message"], &message)
		if tool != CanonicalToolSendMessage || phone != "+15551234567" || message != "Hello from SDR" {
			t.Errorf("payload tool=%q phone=%q message=%q", tool, phone, message)
		}
		for _, forbidden := range []string{"transcript", "pcm", "audio", "conversation_state", "gemini", "jev"} {
			if _, ok := payload[forbidden]; ok {
				t.Errorf("forbidden payload field %q", forbidden)
			}
		}
		_, _ = io.WriteString(w, `{"accepted":true,"message_id":"n8n-msg-42"}`)
	}))
	defer server.Close()
	client, err := New(Config{WebhookURL: server.URL + "/webhook/whatsapp"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.SendMessage(context.Background(), SendMessageRequest{Phone: "+15551234567", Message: "Hello from SDR"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Accepted || got.MessageID != "n8n-msg-42" {
		t.Fatalf("result = %+v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("HTTP calls = %d, want exactly 1", calls.Load())
	}
}

func TestSendMessageRejectsInvalidInputWithoutHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := New(Config{WebhookURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		request SendMessageRequest
	}{
		{name: "empty phone", request: SendMessageRequest{Message: "hello"}},
		{name: "phone CRLF", request: SendMessageRequest{Phone: "+1555\r\nX-Evil: yes", Message: "hello"}},
		{name: "oversized phone", request: SendMessageRequest{Phone: "+" + strings.Repeat("1", MaxPhoneLength), Message: "hello"}},
		{name: "empty message", request: SendMessageRequest{Phone: "+15551234567"}},
		{name: "oversized message", request: SendMessageRequest{Phone: "+15551234567", Message: strings.Repeat("x", MaxMessageLength+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.SendMessage(context.Background(), tc.request)
			if !errors.Is(err, ErrWhatsAppConfiguration) {
				t.Fatalf("error = %v, want configuration/input error", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls.Load())
	}
}

func TestWebhookURLValidation(t *testing.T) {
	for _, raw := range []string{"", "not a url", "ftp://example.test/hook", "http://example.test/hook", "https://user:password@example.test/hook", "https://example.test/hook#fragment"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := New(Config{WebhookURL: raw}); !errors.Is(err, ErrWhatsAppConfiguration) {
				t.Fatalf("error = %v, want configuration error", err)
			}
		})
	}
	for _, raw := range []string{"http://localhost:1234/hook", "http://127.0.0.1:1234/hook", "http://[::1]:1234/hook"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := New(Config{WebhookURL: raw}); err != nil {
				t.Fatalf("loopback URL rejected: %v", err)
			}
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("N8N_WHATSAPP_WEBHOOK_URL", "https://n8n.example.test/webhook/abc")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.WebhookURL != "https://n8n.example.test/webhook/abc" {
		t.Fatalf("webhook URL = %q", config.WebhookURL)
	}
}

func TestSendMessageRejectsHTTPStatusesWithoutLeakingResponse(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "secret=n8n-private-token downstream stack trace")
			}))
			defer server.Close()
			client, err := New(Config{WebhookURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SendMessage(context.Background(), SendMessageRequest{Phone: "+15551234567", Message: "hello"})
			if !errors.Is(err, ErrWhatsAppRejected) || strings.Contains(err.Error(), "n8n-private-token") || strings.Contains(err.Error(), "stack trace") {
				t.Fatalf("response not sanitized: %v", err)
			}
		})
	}
}

func TestSendMessageRejectsInvalidResponsesWithoutLeakingBody(t *testing.T) {
	for _, response := range []string{"<html>internal-secret</html>", "not-json internal-secret", `{"accepted":false,"error":"internal-secret"}`, `{"accepted":"yes"}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			client, err := New(Config{WebhookURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SendMessage(context.Background(), SendMessageRequest{Phone: "+15551234567", Message: "hello"})
			want := ErrWhatsAppInvalidResponse
			if strings.Contains(response, `"accepted":false`) {
				want = ErrWhatsAppRejected
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "internal-secret") {
				t.Fatalf("error = %v, want sanitized %v", err, want)
			}
		})
	}
}

func TestSendMessageTimeoutAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "timeout"
		if cancelCaller {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { close(started); <-release }))
			defer server.Close()
			defer close(release)
			client, err := NewWithTimeout(Config{WebhookURL: server.URL}, 40*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.SendMessage(ctx, SendMessageRequest{Phone: "+15551234567", Message: "hello"})
				done <- err
			}()
			<-started
			if cancelCaller {
				cancel()
			}
			select {
			case err := <-done:
				if cancelCaller && !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
				if !cancelCaller && !errors.Is(err, ErrWhatsAppTimeout) {
					t.Fatalf("error = %v, want timeout", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not terminate")
			}
		})
	}
}

func TestSendMessageTransportFailureIsClassified(t *testing.T) {
	client, err := New(Config{WebhookURL: "https://127.0.0.1:1/webhook"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SendMessage(context.Background(), SendMessageRequest{Phone: "+15551234567", Message: "hello"})
	if !errors.Is(err, ErrWhatsAppTransport) {
		t.Fatalf("error = %v, want transport error", err)
	}
}
