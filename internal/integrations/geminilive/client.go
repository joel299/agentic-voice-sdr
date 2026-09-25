// Package geminilive provides the provider-specific Gemini Live session boundary.
package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"nhooyr.io/websocket"
)

const (
	DefaultEndpoint  = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	DefaultModel     = "gemini-3.8-live"
	InputSampleRate  = 16000
	OutputSampleRate = 24000
)

var ErrNotReady = errors.New("geminilive: session is not ready")

type Config struct {
	APIKey             string
	Model              string
	Endpoint           string
	SystemInstruction  string
	Tools              []ToolDefinition
	ResponseModalities []string
}

type ToolDefinition struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations"`
}

type FunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func ConfigFromEnv() Config {
	return Config{APIKey: os.Getenv("GEMINI_API_KEY"), Model: os.Getenv("GEMINI_LIVE_MODEL"), Endpoint: os.Getenv("GEMINI_LIVE_ENDPOINT")}
}

func (c Config) normalized() Config {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if len(c.ResponseModalities) == 0 {
		c.ResponseModalities = []string{"AUDIO"}
	}
	return c
}

type ErrorKind string

const (
	ErrorAuthentication ErrorKind = "authentication"
	ErrorConnect        ErrorKind = "connect"
	ErrorSetup          ErrorKind = "setup"
	ErrorProtocol       ErrorKind = "protocol"
	ErrorSend           ErrorKind = "send"
	ErrorReceive        ErrorKind = "receive"
	ErrorRemoteClose    ErrorKind = "remote_close"
	ErrorCanceled       ErrorKind = "canceled"
)

type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return "geminilive: " + string(e.Kind)
	}
	return "geminilive: " + string(e.Kind) + ": " + e.Err.Error()
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
func wrap(kind ErrorKind, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Err: err}
}

// Event is a typed subset of Gemini's server message. Unknown valid messages
// are surfaced as EventUnknown rather than being mistaken for a closed socket.
type Event struct {
	Kind          EventKind
	Audio         []byte
	AudioMimeType string
	Text          string
	ToolCalls     []ToolCall
	Error         string
}
type EventKind string

const (
	EventSetupComplete       EventKind = "setup_complete"
	EventAudio               EventKind = "audio"
	EventInputTranscription  EventKind = "input_transcription"
	EventOutputTranscription EventKind = "output_transcription"
	EventTurnComplete        EventKind = "turn_complete"
	EventInterrupted         EventKind = "interrupted"
	EventToolCall            EventKind = "tool_call"
	EventAPIError            EventKind = "api_error"
	EventUnknown             EventKind = "unknown"
	EventClosed              EventKind = "closed"
)

type ToolCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Session struct {
	conn      *websocket.Conn
	cfg       Config
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

func Connect(ctx context.Context, cfg Config) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.normalized()
	if cfg.APIKey == "" {
		return nil, wrap(ErrorAuthentication, errors.New("API key is required"))
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "wss" && u.Scheme != "ws" {
		return nil, wrap(ErrorConnect, errors.New("invalid WebSocket endpoint"))
	}
	if err := validateEndpoint(u); err != nil {
		return nil, wrap(ErrorConnect, err)
	}
	q := u.Query()
	q.Set("key", cfg.APIKey)
	u.RawQuery = q.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, wrap(ErrorCanceled, context.Cause(ctx))
		}
		return nil, wrap(ErrorConnect, errors.New("WebSocket connection failed"))
	}
	s := &Session{conn: conn, cfg: cfg, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	if err := s.sendJSON(ctx, setupMessage(cfg)); err != nil {
		_ = s.Close()
		return nil, wrap(ErrorSetup, err)
	}
	readyCtx := ctx
	msg, err := s.readJSON(readyCtx)
	if err != nil {
		_ = s.Close()
		return nil, wrap(ErrorSetup, err)
	}
	if apiErr := parseAPIError(msg); apiErr != "" {
		_ = s.Close()
		return nil, wrap(ErrorSetup, errors.New("remote setup rejected"))
	}
	if _, ok := msg["setupComplete"]; !ok {
		_ = s.Close()
		return nil, wrap(ErrorSetup, errors.New("setup acknowledgement missing"))
	}
	return s, nil
}

func validateEndpoint(u *url.URL) error {
	if u == nil || u.Hostname() == "" {
		return errors.New("invalid WebSocket endpoint")
	}
	if u.Scheme == "ws" && !isLoopbackHost(u.Hostname()) {
		return errors.New("insecure WebSocket endpoint")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Session) sendJSON(ctx context.Context, message any) error {
	return s.send(ctx, message)
}

func setupMessage(cfg Config) map[string]any {
	// Gemini Live expects generationConfig, rather than responseModalities and
	// transcription settings directly under setup.
	setup := map[string]any{
		"model": "models/" + cfg.Model,
		"generationConfig": map[string]any{
			"responseModalities": cfg.ResponseModalities,
		},
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
	}
	if len(cfg.Tools) > 0 {
		setup["tools"] = cfg.Tools
	}
	if strings.TrimSpace(cfg.SystemInstruction) != "" {
		setup["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": cfg.SystemInstruction}}}
	}
	return map[string]any{"setup": setup}
}

func (s *Session) SendText(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("geminilive: text is empty")
	}
	return s.send(ctx, map[string]any{"realtimeInput": map[string]any{"text": text}})
}
func (s *Session) SendAudio(ctx context.Context, pcm16k []byte) error {
	if len(pcm16k) == 0 {
		return errors.New("geminilive: audio is empty")
	}
	return s.send(ctx, map[string]any{"realtimeInput": map[string]any{"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(pcm16k), "mimeType": "audio/pcm;rate=16000"}}})
}

// EndAudio signals the end of the realtime audio stream while automatic
// voice activity detection remains enabled by the provider.
func (s *Session) EndAudio(ctx context.Context) error {
	return s.send(ctx, map[string]any{"realtimeInput": map[string]any{"audioStreamEnd": true}})
}
func (s *Session) send(ctx context.Context, message any) error {
	if s == nil || s.conn == nil {
		return ErrNotReady
	}
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := json.Marshal(message)
	if err != nil {
		return wrap(ErrorProtocol, err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.Write(ctx, websocket.MessageText, data); err != nil {
		if ctx.Err() != nil {
			return wrap(ErrorCanceled, ctx.Err())
		}
		return wrap(ErrorSend, errors.New("WebSocket write failed"))
	}
	return nil
}
func (s *Session) readJSON(ctx context.Context) (map[string]json.RawMessage, error) {
	typ, data, err := s.conn.Read(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, wrap(ErrorCanceled, ctx.Err())
		}
		return nil, wrap(ErrorReceive, errors.New("WebSocket read failed"))
	}
	if typ != websocket.MessageText && typ != websocket.MessageBinary {
		return nil, wrap(ErrorProtocol, errors.New("unexpected WebSocket frame type"))
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, wrap(ErrorProtocol, errors.New("malformed server JSON"))
	}
	return msg, nil
}
func (s *Session) Receive(ctx context.Context) (Event, error) {
	if s == nil || s.conn == nil {
		return Event{}, ErrNotReady
	}
	if ctx == nil {
		ctx = context.Background()
	}
	msg, err := s.readJSON(ctx)
	if err != nil {
		return Event{}, err
	}
	return parseEvent(msg), nil
}
func parseAPIError(msg map[string]json.RawMessage) string {
	if raw, ok := msg["error"]; ok {
		var v struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &v)
		return v.Message
	}
	return ""
}
func parseEvent(msg map[string]json.RawMessage) Event {
	if parseAPIError(msg) != "" {
		return Event{Kind: EventAPIError, Error: "remote API error"}
	}
	if _, ok := msg["setupComplete"]; ok {
		return Event{Kind: EventSetupComplete}
	}
	if raw, ok := msg["toolCall"]; ok {
		var t struct {
			FunctionCalls []ToolCall `json:"functionCalls"`
		}
		if json.Unmarshal(raw, &t) == nil && len(t.FunctionCalls) > 0 {
			return Event{Kind: EventToolCall, ToolCalls: t.FunctionCalls}
		}
		return Event{Kind: EventToolCall}
	}
	if raw, ok := msg["serverContent"]; ok {
		var c struct {
			ModelTurn struct {
				Parts []struct {
					InlineData struct {
						Data     string `json:"data"`
						MimeType string `json:"mimeType"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"modelTurn"`
			InputTranscription struct {
				Text string `json:"text"`
			} `json:"inputTranscription"`
			InterimInputTranscription struct {
				Text string `json:"text"`
			} `json:"interimInputTranscription"`
			FinalInputTranscription struct {
				Text string `json:"text"`
			} `json:"finalInputTranscription"`
			OutputTranscription struct {
				Text string `json:"text"`
			} `json:"outputTranscription"`
			TurnComplete bool `json:"turnComplete"`
			Interrupted  bool `json:"interrupted"`
		}
		if json.Unmarshal(raw, &c) == nil {
			if c.Interrupted {
				return Event{Kind: EventInterrupted}
			}
			if c.InputTranscription.Text != "" {
				return Event{Kind: EventInputTranscription, Text: c.InputTranscription.Text}
			}
			if c.InterimInputTranscription.Text != "" {
				return Event{Kind: EventInputTranscription, Text: c.InterimInputTranscription.Text}
			}
			if c.FinalInputTranscription.Text != "" {
				return Event{Kind: EventInputTranscription, Text: c.FinalInputTranscription.Text}
			}
			if c.OutputTranscription.Text != "" {
				return Event{Kind: EventOutputTranscription, Text: c.OutputTranscription.Text}
			}
			if len(c.ModelTurn.Parts) > 0 && c.ModelTurn.Parts[0].InlineData.Data != "" {
				b, err := base64.StdEncoding.DecodeString(c.ModelTurn.Parts[0].InlineData.Data)
				if err == nil {
					return Event{Kind: EventAudio, Audio: b, AudioMimeType: c.ModelTurn.Parts[0].InlineData.MimeType}
				}
			}
			if c.TurnComplete {
				return Event{Kind: EventTurnComplete}
			}
		}
	}
	return Event{Kind: EventUnknown}
}
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() { close(s.done); err = s.conn.Close(websocket.StatusNormalClosure, "client closed") })
	return err
}
