package geminilive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if err != nil || e.Kind != EventToolCall || e.ToolCall == nil || e.ToolCall.Name != "schedule" {
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
	if got := parseEvent(map[string]json.RawMessage{"futureField": json.RawMessage(`{}`)}); got.Kind != EventClosed {
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
