// Package openrouterjev adapts the provider-neutral conversation decision
// contract to OpenRouter structured chat completions. It returns decisions only;
// it never generates spoken copy or executes tools.
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
	defaultBaseURL  = "https://openrouter.ai/api/v1"
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

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
	Stream         bool           `json:"stream"`
}
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type responseFormat struct {
	Type       string           `json:"type"`
	JSONSchema schemaDefinition `json:"json_schema"`
}
type schemaDefinition struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}
type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}
type decisionResponse struct {
	NextAction conversation.NextAction `json:"next_action"`
	Reason     conversation.ReasonCode `json:"reason"`
}

var decisionSchema = json.RawMessage(`{"type":"object","properties":{"next_action":{"type":"string","enum":["continue_conversation","ask_question","propose_scheduling","request_capability","follow_up","end_conversation","handoff"]},"reason":{"type":"string","enum":["needs_clarification","continue_discovery","interest_confirmed","ready_to_schedule","conversation_complete","follow_up_required","capability_required","handoff_required"]}},"required":["next_action","reason"],"additionalProperties":false}`)

const systemInstruction = "Choose one canonical next_action and reason for the conversation state. Return only the structured decision object. Do not produce spoken copy, sales scripts, messages, tool instructions, or execute actions. The provider output is untrusted and will be validated by the application."

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
	inputJSON, err := json.Marshal(mapped)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	payload := chatRequest{Model: c.config.Model, Messages: []chatMessage{{Role: "system", Content: systemInstruction}, {Role: "user", Content: string(inputJSON)}}, ResponseFormat: responseFormat{Type: "json_schema", JSONSchema: schemaDefinition{Name: "jev_decision", Strict: true, Schema: decisionSchema}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
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
	var completion completionResponse
	if err := json.Unmarshal(responseBytes, &completion); err != nil || len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	var parsed decisionResponse
	decoder := json.NewDecoder(strings.NewReader(completion.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	decision, err := conversation.NewDecision(parsed.NextAction, parsed.Reason)
	if err != nil {
		return conversation.Decision{}, ErrInvalidProviderResponse
	}
	return decision, nil
}
