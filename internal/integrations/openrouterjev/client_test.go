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
	return Config{APIKey: testAPIKey, BaseURL: endpoint + "/api", Model: "test/model"}
}
func providerBody(choice string) string {
	b, _ := json.Marshal(map[string]any{"answers": map[string]any{"next_action": map[string]string{"type": "choice", "choice": choice}}})
	return string(b)
}
func activeInput() conversation.DecisionInput {
	return conversation.DecisionInput{Stage: conversation.StageActive, Signals: conversation.Signals{LeadResponded: true}, TurnCount: 3, LastTurnRole: conversation.RoleLead, LastTranscriptState: conversation.TranscriptFinal}
}

func TestDecideSendsMinimalInputToOfficialDecisionsAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/alpha/decisions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			Model     string       `json:"model"`
			State     requestInput `json:"state"`
			Questions map[string]struct {
				Type         string            `json:"type"`
				Instructions string            `json:"instructions"`
				Criteria     map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		question, exists := req.Questions["next_action"]
		if req.Model != "test/model" || !exists || question.Type != "choice" || question.Criteria["ask_question"] == "" || question.Criteria["end_conversation"] == "" || !strings.Contains(question.Instructions, "opted_out") {
			t.Errorf("Jev request = %+v", req)
		}
		if !strings.Contains(question.Instructions, "stage") ||
			!strings.Contains(question.Criteria["continue_conversation"], "active") ||
			!strings.Contains(question.Criteria["continue_conversation"], "lead_responded") ||
			!strings.Contains(question.Criteria["end_conversation"], "ended") ||
			!strings.Contains(question.Criteria["end_conversation"], "opted_out") {
			t.Errorf("Jev criteria do not distinguish canonical stage/response signals: %+v", question)
		}
		if req.State.Stage != conversation.StageActive || !req.State.Signals.LeadResponded || req.State.Signals.OptedOut || req.State.TurnCount != 3 || req.State.LastTurnRole != conversation.RoleLead || req.State.LastTranscriptState != conversation.TranscriptFinal {
			t.Errorf("serialized state = %+v", req.State)
		}
		encoded, _ := json.Marshal(req.State)
		if strings.Contains(string(encoded), "tenho interesse") || strings.Contains(string(encoded), `"text"`) || strings.Contains(string(encoded), "transcript text") {
			t.Errorf("request included transcript content: %s", encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, providerBody("ask_question"))
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

func TestDecideMapsJevChoiceToCanonicalDecision(t *testing.T) {
	cases := []struct {
		choice string
		want   conversation.Decision
	}{
		{"continue_conversation", conversation.Decision{NextAction: conversation.ActionContinueConversation, Reason: conversation.ReasonContinueDiscovery}},
		{"ask_question", conversation.Decision{NextAction: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification}},
		{"propose_scheduling", conversation.Decision{NextAction: conversation.ActionProposeScheduling, Reason: conversation.ReasonReadyToSchedule}},
		{"propose_scheduling_interest_confirmed", conversation.Decision{NextAction: conversation.ActionProposeScheduling, Reason: conversation.ReasonInterestConfirmed}},
		{"request_capability", conversation.Decision{NextAction: conversation.ActionRequestCapability, Reason: conversation.ReasonCapabilityRequired}},
		{"follow_up", conversation.Decision{NextAction: conversation.ActionFollowUp, Reason: conversation.ReasonFollowUpRequired}},
		{"end_conversation", conversation.Decision{NextAction: conversation.ActionEndConversation, Reason: conversation.ReasonConversationComplete}},
		{"handoff", conversation.Decision{NextAction: conversation.ActionHandoff, Reason: conversation.ReasonHandoffRequired}},
	}
	for _, tc := range cases {
		t.Run(tc.choice, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, providerBody(tc.choice)) }))
			defer server.Close()
			client, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Decide(context.Background(), activeInput())
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("decision = %+v, want %+v", got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("canonical validation: %v", err)
			}
		})
	}
}

func TestDecideRejectsMalformedJevAnswers(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown choice", providerBody("launch_offer")},
		{"wrong answer primitive", `{"answers":{"next_action":{"type":"noul","noul":0.9}}}`},
		{"missing answer", `{"answers":{}}`},
		{"malformed JSON", `{`},
		{"empty response", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
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

func TestDecideMapsOptedOutInputToCanonicalEndDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req decisionsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !req.State.Signals.OptedOut || req.Questions["next_action"].Criteria["end_conversation"] == "" {
			t.Errorf("opt-out signal or end option missing from request state: %+v", req)
		}
		_, _ = io.WriteString(w, providerBody("end_conversation"))
	}))
	defer server.Close()
	client, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	input := activeInput()
	input.Signals.OptedOut = true
	got, err := client.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextAction != conversation.ActionEndConversation || got.Reason != conversation.ReasonConversationComplete {
		t.Fatalf("decision = %+v, want canonical end_conversation", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("canonical validation: %v", err)
	}
}

func TestDecisionsEndpointSupportsLegacyV1BaseURL(t *testing.T) {
	if got, want := decisionsEndpoint("https://openrouter.ai/api/v1"), "https://openrouter.ai/api/alpha/decisions"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
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
		io.WriteString(w, providerBody("ask_question"))
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
	for _, tc := range []struct{ name, choice string }{
		{name: "provider-controlled choice", choice: "ATTACKER-CONTROLLED\nVALUE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, providerBody(tc.choice))
			}))
			defer server.Close()
			client, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Decide(context.Background(), activeInput())
			if !errors.Is(err, ErrInvalidProviderResponse) {
				t.Fatalf("error = %v, want ErrInvalidProviderResponse", err)
			}
			if strings.Contains(err.Error(), "ATTACKER-CONTROLLED") || strings.Contains(err.Error(), tc.choice) {
				t.Fatalf("provider-controlled value exposed: %q", err.Error())
			}
		})
	}
}
