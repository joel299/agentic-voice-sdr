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
	"acceptance":          salesintent.Acceptance,
	"indecision_cost":     salesintent.IndecisionCost,
	"indecision_security": salesintent.IndecisionSecurity,
	"indecision_timing_or_internal_alignment": salesintent.IndecisionTiming,
	"rejection":                            salesintent.Rejection,
	"opt_out":                              salesintent.OptOut,
	"human_request":                        salesintent.HumanRequest,
	"clarification_or_information_request": salesintent.Clarification,
	"capability_request":                   salesintent.CapabilityRequest,
	"neutral_continue":                     salesintent.NeutralContinue,
}

var semanticIntentQuestion = decisionQuestion{
	Type:         "choice",
	Instructions: "Classify the semantic intent of latest_final_lead_text using the full bounded meaning and conversation context. Return exactly one intent enum. Do not classify by literal keyword rules, do not generate spoken copy or tool instructions. Distinguish an explicit acceptance/meeting readiness from general interest. Use opt_out only when the lead clearly asks to stop contact; human_request only for an explicit request to speak with a person; capability_request only for an explicit product capability question. If meaning is unclear, use clarification_or_information_request or neutral_continue as appropriate. The opted_out context signal is a hard safety invariant.",
	Criteria: map[string]string{
		"acceptance":          "The lead semantically agrees to a proposed next step, accepts a concrete suggested time, or explicitly asks to schedule. Mere interest is not acceptance.",
		"indecision_cost":     "The lead raises price, budget, or cost as an objection without rejecting contact.",
		"indecision_security": "The lead raises privacy, security, compliance, reliability, or trust concerns without rejecting contact.",
		"indecision_timing_or_internal_alignment": "The lead needs more time, internal discussion, or alignment before advancing, including a requested future follow-up.",
		"rejection":                            "The lead clearly declines the offer or says the product/project is not wanted, without asking to stop all contact.",
		"opt_out":                              "The lead explicitly asks to stop, unsubscribe, or not be contacted again.",
		"human_request":                        "The lead explicitly asks for a human, person, representative, or transfer.",
		"clarification_or_information_request": "The lead asks a question or requests explanation/information before deciding.",
		"capability_request":                   "The lead asks whether a specific capability, integration, or function is available.",
		"neutral_continue":                     "The lead acknowledges, gives neutral conversational input, or meaning is insufficient for a more specific class.",
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
	mapped.LatestFinalLeadText = salesintent.SanitizeLeadText(input.LatestFinalLeadText)
	mapped.MatchingExecutableCapability = input.HasMatchingExecutableCapability
	payload := decisionsRequest{
		Model:     c.config.Model,
		State:     mapped,
		Questions: map[string]decisionQuestion{"intent": semanticIntentQuestion},
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
	answer, ok := providerResponse.Answers["intent"]
	if !ok || answer.Type != "choice" {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	intent, ok := intentChoices[answer.Choice]
	if !ok {
		return conversation.Decision{}, ErrInvalidProviderResponse
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
	result, err := salesintent.Decide(intent, intent == salesintent.IndecisionTiming, mapped.MatchingExecutableCapability)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	validated, err := conversation.NewDecision(result.Decision.NextAction, result.Decision.Reason)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	return validated, nil
}
