package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestSessionContractAndEvents(t *testing.T) {
	type observed struct {
		setup, text, audio, audioStreamEnd, activityEnd bool
		key                                             string
	}
	got := make(chan observed, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "secret-not-for-logs" {
			t.Errorf("API key was not passed to endpoint")
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := context.Background()
		_, raw, err := c.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		var setup map[string]any
		if json.Unmarshal(raw, &setup) != nil {
			t.Error("setup was not JSON")
		}
		setupRaw, _ := json.Marshal(setup)
		if !strings.Contains(string(setupRaw), `"setup"`) {
			t.Error("first payload was not setup")
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"setupComplete":{}}`))
		var o observed
		o.setup = true
		o.key = r.URL.Query().Get("key")
		for i := 0; i < 3; i++ {
			_, raw, err = c.Read(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			var msg map[string]any
			_ = json.Unmarshal(raw, &msg)
			b, _ := json.Marshal(msg)
			if strings.Contains(string(b), `"text":"hello"`) {
				o.text = true
			}
			if strings.Contains(string(b), `audio/pcm;rate=16000`) {
				o.audio = true
			}
			var realtime struct {
				AudioStreamEnd bool            `json:"audioStreamEnd"`
				ActivityEnd    json.RawMessage `json:"activityEnd"`
			}
			if realtimeRaw, ok := msg["realtimeInput"].(map[string]any); ok {
				rawRealtime, _ := json.Marshal(realtimeRaw)
				_ = json.Unmarshal(rawRealtime, &realtime)
			}
			o.audioStreamEnd = realtime.AudioStreamEnd
			o.activityEnd = len(realtime.ActivityEnd) > 0
		}
		got <- o
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"serverContent":{"outputTranscription":{"text":"hello back"}}}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQID"}}]}}}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"serverContent":{"interrupted":true}}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"toolCall":{"functionCalls":[{"id":"1","name":"schedule","args":{"x":1}}]}}`))
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	endpoint := "ws" + strings.TrimPrefix(ts.URL, "http")
	s, err := Connect(context.Background(), Config{APIKey: "secret-not-for-logs", Endpoint: endpoint, Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SendText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err = s.SendAudio(context.Background(), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err = s.EndAudio(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := <-got; !o.setup || !o.text || !o.audio || !o.audioStreamEnd || o.activityEnd {
		t.Fatalf("server observations: %+v", o)
	}
	e, err := s.Receive(context.Background())
	if err != nil || e.Kind != EventOutputTranscription || e.Text != "hello back" {
		t.Fatalf("text event: %+v %v", e, err)
	}
	e, err = s.Receive(context.Background())
	if err != nil || e.Kind != EventAudio || len(e.Audio) != 3 || e.AudioMimeType != "audio/pcm;rate=24000" {
		t.Fatalf("audio event: %+v %v", e, err)
	}
	e, err = s.Receive(context.Background())
	if err != nil || e.Kind != EventInterrupted {
		t.Fatalf("interrupt event: %+v %v", e, err)
	}
	e, err = s.Receive(context.Background())
	if err != nil || e.Kind != EventToolCall || len(e.ToolCalls) != 1 || e.ToolCalls[0].Name != "schedule" {
		t.Fatalf("tool event: %+v %v", e, err)
	}
}

func TestSessionErrorsCancellationAndSecretRedaction(t *testing.T) {
	if _, err := Connect(context.Background(), Config{}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected sanitized auth error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Connect(ctx, Config{APIKey: "secret"}); err == nil {
		t.Fatal("expected cancellation")
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := websocket.Accept(w, r, nil)
		defer c.Close(websocket.StatusNormalClosure, "")
		_, _, _ = c.Read(context.Background())
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	connectCtx, connectCancel := context.WithTimeout(context.Background(), time.Second)
	defer connectCancel()
	_, err := Connect(connectCtx, Config{APIKey: "secret", Endpoint: "ws" + strings.TrimPrefix(ts.URL, "http")})
	if err == nil {
		t.Fatal("expected setup failure")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		_ = time.Now()
	}
	_ = base64.StdEncoding
}

func TestParseUnknownAndMalformedServerMessages(t *testing.T) {
	if got := parseEvent(map[string]json.RawMessage{"futureField": json.RawMessage(`{}`)}); got.Kind != EventUnknown {
		t.Fatalf("unknown event: %+v", got)
	}
	if parseAPIError(map[string]json.RawMessage{"error": json.RawMessage(`{"message":"secret detail"}`)}) != "secret detail" {
		t.Fatal("API error parser")
	}
	if got := parseEvent(map[string]json.RawMessage{"serverContent": json.RawMessage(`{"interimInputTranscription":{"text":"hello"}}`)}); got.Kind != EventInputTranscription || got.Text != "hello" {
		t.Fatalf("interim input transcription: %+v", got)
	}
}

func TestSetupMessageUsesLiveAPIEnvelope(t *testing.T) {
	msg := setupMessage(Config{Model: "test-model", Tools: []ToolDefinition{{FunctionDeclarations: []FunctionDeclaration{{Name: "schedule"}}}}})
	setup, ok := msg["setup"].(map[string]any)
	if !ok {
		t.Fatal("setup envelope missing")
	}
	generation, ok := setup["generationConfig"].(map[string]any)
	if !ok || generation["responseModalities"] == nil {
		t.Fatalf("generation config missing: %#v", setup)
	}
	if _, ok := setup["tools"]; !ok {
		t.Fatal("tools missing from setup")
	}
	if _, ok := setup["responseModalities"]; ok {
		t.Fatal("response modalities must be nested in generationConfig")
	}
}

func TestEndpointTransportPolicy(t *testing.T) {
	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "remote wss", address: "wss://remote-host/live"},
		{name: "localhost ws", address: "ws://localhost/live"},
		{name: "ipv4 loopback ws", address: "ws://127.0.0.1/live"},
		{name: "ipv6 loopback ws", address: "ws://[::1]/live"},
		{name: "remote ws", address: "ws://remote-host/live?key=must-not-leak", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.address)
			if err != nil {
				t.Fatal(err)
			}
			err = validateEndpoint(u)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateEndpoint() error = %v, wantErr %t", err, tt.wantErr)
			}
			if tt.wantErr && (strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "secret")) {
				t.Fatalf("endpoint error leaked secret: %v", err)
			}
		})
	}
}

func TestParseInputTranscriptFinality(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		state InputTranscriptState
		text  string
	}{
		{name: "interim", raw: `{"interimInputTranscription":{"text":"draft"}}`, state: TranscriptInterim, text: "draft"},
		{name: "final", raw: `{"inputTranscription":{"text":"committed"}}`, state: TranscriptFinal, text: "committed"},
		{name: "defensive final", raw: `{"finalInputTranscription":{"text":"committed"}}`, state: TranscriptFinal, text: "committed"},
		{name: "final wins", raw: `{"interimInputTranscription":{"text":"draft"},"inputTranscription":{"text":"committed"}}`, state: TranscriptFinal, text: "committed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEvent(map[string]json.RawMessage{"serverContent": json.RawMessage(tt.raw)})
			if got.Kind != EventInputTranscription || got.InputTranscriptState != tt.state || got.Text != tt.text {
				t.Fatalf("event = %+v, want state %q text %q", got, tt.state, tt.text)
			}
		})
	}
}

func TestParseInputTranscriptEmptyDoesNotBecomeFinal(t *testing.T) {
	for _, raw := range []string{
		`{"interimInputTranscription":{"text":""}}`,
		`{"inputTranscription":{"text":""}}`,
		`{"finalInputTranscription":{"text":""}}`,
	} {
		got := parseEvent(map[string]json.RawMessage{"serverContent": json.RawMessage(raw)})
		if got.Kind == EventInputTranscription && got.InputTranscriptState == TranscriptFinal {
			t.Fatalf("empty transcription became final: %+v", got)
		}
	}
}

func TestOutputTurnCompleteAndOtherEventsDoNotSetInputTranscriptState(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind EventKind
	}{
		{name: "output", raw: `{"outputTranscription":{"text":"response"}}`, kind: EventOutputTranscription},
		{name: "turn complete", raw: `{"turnComplete":true}`, kind: EventTurnComplete},
		{name: "audio", raw: `{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm","data":"AQID"}}]}}`, kind: EventAudio},
		{name: "interrupted", raw: `{"interrupted":true}`, kind: EventInterrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEvent(map[string]json.RawMessage{"serverContent": json.RawMessage(tt.raw)})
			if got.Kind != tt.kind || got.InputTranscriptState != TranscriptNone {
				t.Fatalf("event = %+v, want kind %q and no input state", got, tt.kind)
			}
		})
	}
}

func TestParsePreservesAllToolCalls(t *testing.T) {
	got := parseEvent(map[string]json.RawMessage{"toolCall": json.RawMessage(`{"functionCalls":[{"id":"1","name":"schedule","args":{"when":"tomorrow"}},{"id":"2","name":"send_message","args":{"text":"hello"}}]}`)})
	if got.Kind != EventToolCall || len(got.ToolCalls) != 2 || got.InputTranscriptState != TranscriptNone {
		t.Fatalf("event = %+v", got)
	}
	if got.ToolCalls[0].ID != "1" || got.ToolCalls[0].Name != "schedule" || got.ToolCalls[1].ID != "2" || got.ToolCalls[1].Name != "send_message" {
		t.Fatalf("tool call order/identity: %+v", got.ToolCalls)
	}
	if !strings.Contains(string(got.ToolCalls[0].Args), "tomorrow") || !strings.Contains(string(got.ToolCalls[1].Args), "hello") {
		t.Fatalf("tool call args: %+v", got.ToolCalls)
	}
}
