package openrouterjev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
)

const testAPIKey = "test-secret-key"

func testConfig(endpoint string) Config {
	return Config{APIKey: testAPIKey, BaseURL: endpoint + "/api/v1", Model: "test/model"}
}
func providerBody(content string) string {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	return string(b)
}
func activeInput() conversation.DecisionInput {
	return conversation.DecisionInput{Stage: conversation.StageActive, Signals: conversation.Signals{LeadResponded: true}, TurnCount: 3, LastTurnRole: conversation.RoleLead, LastTranscriptState: conversation.TranscriptFinal}
}

func TestDecideSendsMinimalInputAndStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Model != "test/model" || req.ResponseFormat.Type != "json_schema" || req.ResponseFormat.JSONSchema.Name != "jev_decision" || !req.ResponseFormat.JSONSchema.Strict {
			t.Errorf("request config = %+v", req)
		}
		if len(req.Messages) != 2 || req.Messages[1].Role != "user" {
			t.Errorf("messages = %+v", req.Messages)
			return
		}
		var input struct {
			Stage   string `json:"stage"`
			Signals struct {
				LeadResponded bool `json:"lead_responded"`
				OptedOut      bool `json:"opted_out"`
			} `json:"signals"`
			TurnCount           int    `json:"turn_count"`
			LastTurnRole        string `json:"last_turn_role"`
			LastTranscriptState string `json:"last_transcript_state"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
			t.Errorf("input JSON: %v", err)
		}
		if input.Stage != "active" || !input.Signals.LeadResponded || input.Signals.OptedOut || input.TurnCount != 3 || input.LastTurnRole != "lead" || input.LastTranscriptState != "final" {
			t.Errorf("serialized input = %+v", input)
		}
		if strings.Contains(req.Messages[1].Content, "tenho interesse") || strings.Contains(req.Messages[1].Content, `"text"`) {
			t.Errorf("request included transcript: %s", req.Messages[1].Content)
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, providerBody(`{"next_action":"ask_question","reason":"needs_clarification"}`))
	}))
	defer server.Close()
	client, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Decide(context.Background(), activeInput())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := conversation.NewDecision(conversation.ActionAskQuestion, conversation.ReasonNeedsClarification)
	if got != want {
		t.Fatalf("decision = %+v, want %+v", got, want)
	}
}

func TestDecideRejectsInvalidProviderDecisions(t *testing.T) {
	for _, tc := range []struct{ name, content string }{{"unknown action", `{"next_action":"launch_offer","reason":"needs_clarification"}`}, {"unknown reason", `{"next_action":"ask_question","reason":"unknown_reason"}`}, {"incompatible pair", `{"next_action":"ask_question","reason":"conversation_complete"}`}, {"malformed JSON", `{`}, {"empty", ``}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, providerBody(tc.content)) }))
			defer server.Close()
			client, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Decide(context.Background(), activeInput()); !errors.Is(err, ErrInvalidProviderResponse) {
				t.Fatalf("error = %v, want invalid provider response", err)
			}
		})
	}
}

func TestProviderErrorsAreSanitized(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, "upstream echoed "+testAPIKey)
			}))
			defer server.Close()
			client, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Decide(context.Background(), activeInput())
			if err == nil || strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), "upstream echoed") {
				t.Fatalf("unsanitized error: %v", err)
			}
			if !errors.Is(err, ErrProviderRejected) {
				t.Fatalf("error = %v, want provider rejection", err)
			}
		})
	}
}

func TestDecideTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		io.WriteString(w, providerBody(`{"next_action":"ask_question","reason":"needs_clarification"}`))
	}))
	defer server.Close()
	client, err := NewWithTimeout(testConfig(server.URL), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Decide(context.Background(), activeInput())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
	close(release)
}

func TestDecideCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		defer close(handlerDone)
		<-release
	}))
	defer server.Close()
	client, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.Decide(ctx, activeInput()); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not cancel promptly")
	}
	close(release)
	<-handlerDone
}

func TestConfigRequiresKeyAndModel(t *testing.T) {
	if _, err := New(Config{BaseURL: "https://openrouter.ai/api/v1"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing credentials error = %v", err)
	}
}

func TestConfigFromEnvRequiresExplicitModel(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "configured-test-key")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("OPENROUTER_JEV_MODEL", "")
	if _, err := ConfigFromEnv(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("error = %v, want configuration error for missing model", err)
	}
	t.Setenv("OPENROUTER_JEV_MODEL", "configured/model")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Model != "configured/model" || config.BaseURL != defaultBaseURL {
		t.Fatalf("config = %+v", config)
	}
}

func stalledBodyServer(t *testing.T) (*httptest.Server, <-chan struct{}, chan struct{}) {
	t.Helper()
	headersSent := make(chan struct{})
	releaseBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headersSent)
		<-releaseBody
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	return server, headersSent, releaseBody
}

func TestBodyReadTimeoutAfterHeaders(t *testing.T) {
	server, headersSent, releaseBody := stalledBodyServer(t)
	defer server.Close()
	defer close(releaseBody)
	client, err := NewWithTimeout(testConfig(server.URL), 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := client.Decide(context.Background(), activeInput()); done <- err }()
	<-headersSent
	select {
	case err := <-done:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("error = %v, want ErrTimeout", err)
		}
		if errors.Is(err, ErrTransport) {
			t.Fatalf("body timeout misclassified as transport: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("body read did not honor timeout")
	}
}

func TestCallerCancellationAfterHeadersDuringBodyRead(t *testing.T) {
	server, headersSent, releaseBody := stalledBodyServer(t)
	defer server.Close()
	defer close(releaseBody)
	client, err := NewWithTimeout(testConfig(server.URL), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Decide(ctx, activeInput()); done <- err }()
	<-headersSent
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want caller cancellation", err)
		}
		if errors.Is(err, ErrTransport) {
			t.Fatalf("caller cancellation misclassified as transport: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("body read did not honor caller cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("synthetic body read failure") }
func (failingBody) Close() error             { return nil }

func TestBodyReadTransportErrorWithoutContextError(t *testing.T) {
	client, err := New(testConfig("https://openrouter.ai/api/v1"))
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: failingBody{}}, nil
	})
	_, err = client.Decide(context.Background(), activeInput())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("error = %v, want ErrTransport", err)
	}
}

func TestInvalidProviderDecisionValuesAreSanitized(t *testing.T) {
	for _, tc := range []struct{ name, action, reason string }{
		{name: "malicious action", action: "ATTACKER-CONTROLLED\nVALUE", reason: "needs_clarification"},
		{name: "malicious reason", action: "ask_question", reason: "ATTACKER-CONTROLLED\nVALUE"},
		{name: "large action", action: "ATTACKER-CONTROLLED-" + strings.Repeat("x", 4096), reason: "needs_clarification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := json.Marshal(map[string]string{"next_action": tc.action, "reason": tc.reason})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, providerBody(string(content))) }))
			defer server.Close()
			client, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Decide(context.Background(), activeInput())
			if !errors.Is(err, ErrInvalidProviderResponse) {
				t.Fatalf("error = %v, want ErrInvalidProviderResponse", err)
			}
			if strings.Contains(err.Error(), "ATTACKER-CONTROLLED") || strings.Contains(err.Error(), tc.action) || strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("provider-controlled value exposed: %q", err.Error())
			}
		})
	}
}
