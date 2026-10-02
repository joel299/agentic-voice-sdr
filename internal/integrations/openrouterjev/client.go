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
	"github.com/joel299/agentic-voice-sdr/internal/domain/salesintent"
)

const (
	defaultBaseURL            = "https://openrouter.ai/api"
	defaultTimeout            = 1500 * time.Millisecond
	CanonicalDefaultTimeoutMS = 1500
	maxResponseSize           = 1 << 20
)

var (
	ErrConfiguration           = errors.New("openrouter JEV configuration is invalid")
	ErrTimeout                 = errors.New("openrouter JEV request timed out")
	ErrProviderRejected        = errors.New("openrouter JEV provider rejected the request")
	ErrTransport               = errors.New("openrouter JEV transport failed")
	ErrInvalidProviderResponse = errors.New("openrouter JEV returned an invalid decision response")
)

// Config contains provider settings. APIKey is never included in returned errors.
type Config struct{ APIKey, BaseURL, Model, Description, DecisionGuidance string }

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

// New creates a client with the bounded 1500ms provider timeout.
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
	TurnCount                    int                          `json:"turn_count"`
	LastTurnRole                 conversation.ParticipantRole `json:"last_turn_role,omitempty"`
	LastTranscriptState          conversation.TranscriptState `json:"last_transcript_state,omitempty"`
	LatestFinalLeadText          string                       `json:"latest_final_lead_text,omitempty"`
	MatchingExecutableCapability bool                         `json:"matching_executable_capability"`
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

var intentChoices = map[string]salesintent.Class{
	"acceptance":                           salesintent.Acceptance,
	"indecision_cost":                      salesintent.IndecisionCost,
	"indecision_security":                  salesintent.IndecisionSecurity,
	"internal_alignment":                   salesintent.InternalAlignment,
	"explicit_future_follow_up":            salesintent.FutureFollowUp,
	"rejection":                            salesintent.Rejection,
	"opt_out":                              salesintent.OptOut,
	"human_request":                        salesintent.HumanRequest,
	"clarification_or_information_request": salesintent.Clarification,
	"capability_request":                   salesintent.CapabilityRequest,
	"neutral_continue":                     salesintent.NeutralContinue,
}

var semanticIntentQuestion = decisionQuestion{
	Type:         "choice",
	Instructions: "Classify the latest final lead text by semantic intent in context; choose exactly one enum, not by keywords. Do not write spoken copy or select tools. Acceptance requires agreement to a concrete next step or scheduling; interest alone is insufficient. internal_alignment means internal review without a request for later contact; explicit_future_follow_up requires a request for later contact. rejection declines the offer but allows contact; opt_out asks to stop contact. human_request asks for a person; capability_request asks whether a specific function exists. Respect opted_out as a hard override. If unclear, choose clarification_or_information_request or neutral_continue.",
	Criteria: map[string]string{
		"acceptance":                           "Accepts a concrete suggested time or next step, or asks to schedule; interest alone is insufficient.",
		"indecision_cost":                      "Raises a price, budget, or affordability concern.",
		"indecision_security":                  "Raises a security, privacy, compliance, or trust concern.",
		"internal_alignment":                   "Needs internal discussion or approval, without asking for later contact.",
		"explicit_future_follow_up":            "Explicitly asks for later contact, tied to a date/event or otherwise.",
		"rejection":                            "Declines the offer but does not ask to stop contact.",
		"opt_out":                              "Asks to stop contact, unsubscribe, or receive no further messages.",
		"human_request":                        "Asks for a human representative, person, or transfer.",
		"clarification_or_information_request": "Asks for explanation or information before deciding.",
		"capability_request":                   "Asks whether a specific feature or integration exists.",
		"neutral_continue":                     "Neutral acknowledgment or insufficient meaning for a specific intent.",
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
	result, err := c.DecideDetailed(ctx, input)
	return result.Decision, err
}

// DetailedResult contains only the validated intent enum and canonical decision.
type DetailedResult struct {
	Intent   salesintent.Class
	Decision conversation.Decision
}

func (c *Client) DecideDetailed(ctx context.Context, input conversation.DecisionInput) (DetailedResult, error) {
	if ctx == nil {
		return DetailedResult{}, ErrConfiguration
	}
	var mapped requestInput
	mapped.Stage = input.Stage
	mapped.Signals.LeadResponded = input.Signals.LeadResponded
	mapped.Signals.OptedOut = input.Signals.OptedOut
	mapped.TurnCount = input.TurnCount
	mapped.LastTurnRole = input.LastTurnRole
	mapped.LastTranscriptState = input.LastTranscriptState
	mapped.LatestFinalLeadText = salesintent.SanitizeLeadText(input.LatestFinalLeadText)
	mapped.MatchingExecutableCapability = input.HasMatchingExecutableCapability
	question := decisionQuestion{Type: semanticIntentQuestion.Type, Instructions: semanticIntentQuestion.Instructions, Criteria: semanticIntentQuestion.Criteria}
	if strings.TrimSpace(c.config.Description) != "" {
		question.Instructions += " Classifier description: " + strings.TrimSpace(c.config.Description)
	}
	if strings.TrimSpace(c.config.DecisionGuidance) != "" {
		question.Instructions += " Owner decision guidance: " + strings.TrimSpace(c.config.DecisionGuidance)
	}
	payload := decisionsRequest{
		Model:     c.config.Model,
		State:     mapped,
		Questions: map[string]decisionQuestion{"intent": question},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, decisionsEndpoint(c.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return DetailedResult{}, ErrConfiguration
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return DetailedResult{}, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return DetailedResult{}, ErrTimeout
		}
		return DetailedResult{}, ErrTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DetailedResult{}, ErrProviderRejected
	}
	responseBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return DetailedResult{}, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return DetailedResult{}, ErrTimeout
		}
		return DetailedResult{}, ErrTransport
	}
	if len(responseBytes) == 0 || len(responseBytes) > maxResponseSize {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	var providerResponse decisionsResponse
	if err := json.Unmarshal(responseBytes, &providerResponse); err != nil {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	answer, ok := providerResponse.Answers["intent"]
	if !ok || answer.Type != "choice" {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	intent, ok := intentChoices[answer.Choice]
	if !ok {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	optedOut := mapped.Signals.OptedOut || input.LastTurnRole == conversation.RoleLead && input.LastTranscriptState == conversation.TranscriptFinal && salesintent.HasExplicitOptOut(mapped.LatestFinalLeadText)
	if optedOut {
		intent = salesintent.OptOut
	}
	if !optedOut && input.Stage == conversation.StageOpening {
		intent = salesintent.NeutralContinue
	} else if !optedOut && (input.Stage == conversation.StageClosing || input.Stage == conversation.StageEnded) {
		intent = salesintent.Rejection
	}
	result, err := salesintent.Decide(intent, mapped.MatchingExecutableCapability)
	if err != nil {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	validated, err := conversation.NewDecision(result.Decision.NextAction, result.Decision.Reason)
	if err != nil {
		return DetailedResult{}, ErrInvalidProviderResponse
	}
	return DetailedResult{Intent: intent, Decision: validated}, nil
}
