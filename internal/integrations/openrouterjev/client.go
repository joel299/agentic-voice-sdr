// Package openrouterjev adapts the provider-neutral conversation decision
// contract to OpenRouter's typed Decisions API. It returns canonical decisions
// only; it never generates spoken copy or executes tools.
package openrouterjev

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

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
)

const (
	defaultBaseURL  = "https://openrouter.ai/api"
	defaultTimeout  = 400 * time.Millisecond
	maxResponseSize = 1 << 20
)

var (
	ErrConfiguration           = errors.New("openrouter JEV configuration is invalid")
	ErrTimeout                 = errors.New("openrouter JEV request timed out")
	ErrProviderRejected        = errors.New("openrouter JEV provider rejected the request")
	ErrTransport               = errors.New("openrouter JEV transport failed")
	ErrInvalidProviderResponse = errors.New("openrouter JEV returned an invalid decision response")
)

// Config contains provider settings. APIKey is never included in returned errors.
type Config struct{ APIKey, BaseURL, Model string }

// ConfigFromEnv loads OpenRouter settings. A model is mandatory; no model ID is
// selected implicitly. The official OpenRouter API base URL is used if omitted.
func ConfigFromEnv() (Config, error) {
	return NewConfig(Config{APIKey: os.Getenv("OPENROUTER_API_KEY"), BaseURL: os.Getenv("OPENROUTER_BASE_URL"), Model: os.Getenv("OPENROUTER_JEV_MODEL")})
}

// NewConfig validates and normalizes provider configuration.
func NewConfig(config Config) (Config, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.APIKey == "" || config.Model == "" {
		return Config{}, ErrConfiguration
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, ErrConfiguration
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return Config{}, ErrConfiguration
		}
	}
	return config, nil
}

type Client struct {
	config     Config
	httpClient *http.Client
	timeout    time.Duration
}

// New creates a client with the bounded 400ms provider timeout.
func New(config Config) (*Client, error) { return NewWithTimeout(config, defaultTimeout) }

// NewWithTimeout permits deterministic timeout tests and stricter deployments.
func NewWithTimeout(config Config, timeout time.Duration) (*Client, error) {
	validated, err := NewConfig(config)
	if err != nil || timeout <= 0 {
		return nil, ErrConfiguration
	}
	return &Client{config: validated, httpClient: &http.Client{}, timeout: timeout}, nil
}

type requestInput struct {
	Stage   conversation.ConversationStage `json:"stage"`
	Signals struct {
		LeadResponded bool `json:"lead_responded"`
		OptedOut      bool `json:"opted_out"`
	} `json:"signals"`
	TurnCount           int                          `json:"turn_count"`
	LastTurnRole        conversation.ParticipantRole `json:"last_turn_role,omitempty"`
	LastTranscriptState conversation.TranscriptState `json:"last_transcript_state,omitempty"`
}

type decisionsRequest struct {
	Model     string                      `json:"model"`
	State     requestInput                `json:"state"`
	Questions map[string]decisionQuestion `json:"questions"`
}

type decisionQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type decisionsResponse struct {
	Answers map[string]typedDecisionAnswer `json:"answers"`
}

type typedDecisionAnswer struct {
	Type   string `json:"type"`
	Choice string `json:"choice"`
}

var canonicalDecisionChoices = map[string]conversation.Decision{
	"continue_conversation":                 {NextAction: conversation.ActionContinueConversation, Reason: conversation.ReasonContinueDiscovery},
	"ask_question":                          {NextAction: conversation.ActionAskQuestion, Reason: conversation.ReasonNeedsClarification},
	"propose_scheduling":                    {NextAction: conversation.ActionProposeScheduling, Reason: conversation.ReasonReadyToSchedule},
	"propose_scheduling_interest_confirmed": {NextAction: conversation.ActionProposeScheduling, Reason: conversation.ReasonInterestConfirmed},
	"request_capability":                    {NextAction: conversation.ActionRequestCapability, Reason: conversation.ReasonCapabilityRequired},
	"follow_up":                             {NextAction: conversation.ActionFollowUp, Reason: conversation.ReasonFollowUpRequired},
	"end_conversation":                      {NextAction: conversation.ActionEndConversation, Reason: conversation.ReasonConversationComplete},
	"handoff":                               {NextAction: conversation.ActionHandoff, Reason: conversation.ReasonHandoffRequired},
}

var canonicalNextActionQuestion = decisionQuestion{
	Type:         "choice",
	Instructions: "Choose exactly one canonical next action from the supplied conversation state. If opted_out is true, choose end_conversation. Use only the structured state; do not generate text, spoken copy, messages, or tool instructions.",
	Criteria: map[string]string{
		"continue_conversation":                 "Continue discovery when the active conversation should proceed without a specific clarification.",
		"ask_question":                          "Ask a focused clarifying question when the state indicates clarification is needed.",
		"propose_scheduling":                    "Propose scheduling when the lead is ready to schedule.",
		"propose_scheduling_interest_confirmed": "Propose scheduling when interest is confirmed and scheduling is the appropriate next step.",
		"request_capability":                    "Request a capability when the next step requires an external capability.",
		"follow_up":                             "Choose follow-up when the conversation state calls for a later follow-up.",
		"end_conversation":                      "End the conversation when complete or when the lead has opted out.",
		"handoff":                               "Hand off when the state requires human assistance.",
	},
}

func decisionsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	// OPENROUTER_BASE_URL was historically configured with the /api/v1
	// REST base. The Decisions API is hosted at /api/alpha/decisions.
	if strings.HasSuffix(baseURL, "/api/v1") {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}
	return baseURL + "/alpha/decisions"
}

// Decide sends only the typed decision snapshot and validates the provider result
// using the domain's canonical NewDecision constructor.
func (c *Client) Decide(ctx context.Context, input conversation.DecisionInput) (conversation.Decision, error) {
	if ctx == nil {
		return conversation.Decision{}, ErrConfiguration
	}
	var mapped requestInput
	mapped.Stage = input.Stage
	mapped.Signals.LeadResponded = input.Signals.LeadResponded
	mapped.Signals.OptedOut = input.Signals.OptedOut
	mapped.TurnCount = input.TurnCount
	mapped.LastTurnRole = input.LastTurnRole
	mapped.LastTranscriptState = input.LastTranscriptState
	payload := decisionsRequest{
		Model:     c.config.Model,
		State:     mapped,
		Questions: map[string]decisionQuestion{"next_action": canonicalNextActionQuestion},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, decisionsEndpoint(c.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return conversation.Decision{}, ErrConfiguration
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return conversation.Decision{}, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return conversation.Decision{}, ErrTimeout
		}
		return conversation.Decision{}, ErrTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return conversation.Decision{}, ErrProviderRejected
	}
	responseBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return conversation.Decision{}, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return conversation.Decision{}, ErrTimeout
		}
		return conversation.Decision{}, ErrTransport
	}
	if len(responseBytes) == 0 || len(responseBytes) > maxResponseSize {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	var providerResponse decisionsResponse
	if err := json.Unmarshal(responseBytes, &providerResponse); err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	answer, ok := providerResponse.Answers["next_action"]
	if !ok || answer.Type != "choice" {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	decision, ok := canonicalDecisionChoices[answer.Choice]
	if !ok {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	validated, err := conversation.NewDecision(decision.NextAction, decision.Reason)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	return validated, nil
}
