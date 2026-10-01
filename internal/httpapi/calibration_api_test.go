package httpapi

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
)

func calibrationTestRouter(t *testing.T) http.Handler {
	t.Helper()
	auth, err := NewStaticBearerAuthorizer("owner-test-token")
	if err != nil {
		t.Fatal(err)
	}
	prompt := newHTTPPrompt(t, 1, "Owner prompt", "Safe baseline", true, time.Now())
	manager := &agentPromptManagerFake{active: prompt, versions: []agentprompt.PromptVersion{prompt}}
	store, err := NewTuningStore(JEVSettings{Model: "typesafe/jev-1.13", TimeoutMS: 400, Description: "classifier", DecisionGuidance: "typed decision"}, GeminiSettings{Model: "gemini-3.8-live", VoiceName: "Kore", Description: "consultative", Style: "calm"})
	if err != nil {
		t.Fatal(err)
	}
	deps := CalibrationServices{Prompts: manager, Tuning: store, TestJEV: func(_ context.Context, text, stage string) (JEVTestResult, error) {
		return JEVTestResult{Intent: "internal_alignment", NextAction: "continue_conversation", Reason: "continue_discovery", LatencyMS: 12, TimeoutMS: 400, ProviderStatusClass: "ok"}, nil
	}, TestAgentTurn: func(context.Context, string, string, string) (AgentTurnResult, []byte, error) {
		return AgentTurnResult{TestID: "test-a", Gemini: GeminiTurnMetadata{GenerationComplete: true, TurnComplete: true}}, []byte{0, 0, 1, 0}, nil
	}, RuntimeStatus: func(context.Context) (map[string]any, error) { return map[string]any{"api_ready": true}, nil }}
	r := chi.NewRouter()
	registerCalibrationRoutes(r, auth, deps)
	return r
}

func ownerRequest(handler http.Handler, method, path, body string, authorized bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorized {
		req.Header.Set("Authorization", "Bearer owner-test-token")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestCalibrationEndpointsAreOwnerOnlyAndKeepSecretsOut(t *testing.T) {
	h := calibrationTestRouter(t)
	if got := ownerRequest(h, http.MethodGet, "/v1/config/jev", "", false).Code; got != 401 {
		t.Fatalf("owner auth status=%d", got)
	}
	got := ownerRequest(h, http.MethodGet, "/v1/config/jev", "", true)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"canonical_default_timeout_ms":400`) {
		t.Fatalf("JEV config response %d %s", got.Code, got.Body.String())
	}
	if strings.Contains(strings.ToLower(got.Body.String()), "api_key") {
		t.Fatal("JEV response exposed key field")
	}
	bad := ownerRequest(h, http.MethodPut, "/v1/config/jev", `{"model":"m","timeout_ms":400,"description":"x","decision_guidance":"y","api_key":"secret"}`, true)
	if bad.Code != 400 {
		t.Fatalf("unknown secret field should be rejected, status=%d", bad.Code)
	}
	good := ownerRequest(h, http.MethodPut, "/v1/config/jev", `{"model":"m","timeout_ms":400,"description":"x","decision_guidance":"y"}`, true)
	if good.Code != 200 {
		t.Fatalf("valid JEV tuning status=%d body=%s", good.Code, good.Body.String())
	}
}

func TestExistingPromptServiceRoutesAreMountedBehindOwnerAuth(t *testing.T) {
	h := calibrationTestRouter(t)
	unauth := ownerRequest(h, http.MethodGet, "/api/v1/agent/prompt", "", false)
	if unauth.Code != 401 {
		t.Fatalf("prompt unauth status=%d body=%s", unauth.Code, unauth.Body.String())
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{{http.MethodGet, "/api/v1/agent/prompt", "", 200}, {http.MethodPut, "/api/v1/agent/prompt", `{"name":"new","prompt":"new prompt"}`, 201}, {http.MethodGet, "/api/v1/agent/prompt/versions", "", 200}, {http.MethodPost, "/api/v1/agent/prompt/versions/1/activate", "", 200}} {
		res := ownerRequest(h, tc.method, tc.path, tc.body, true)
		if res.Code != tc.want {
			t.Errorf("%s %s status=%d body=%s want=%d", tc.method, tc.path, res.Code, res.Body.String(), tc.want)
		}
	}
}

func TestCalibrationJEVTurnAndEphemeralWAV(t *testing.T) {
	h := calibrationTestRouter(t)
	jev := ownerRequest(h, http.MethodPost, "/v1/test/jev", `{"lead_text":"Preciso conversar com meu sócio primeiro."}`, true)
	if jev.Code != 200 || !strings.Contains(jev.Body.String(), `"intent":"internal_alignment"`) {
		t.Fatalf("JEV test status=%d body=%s", jev.Code, jev.Body.String())
	}
	turn := ownerRequest(h, http.MethodPost, "/v1/test/agent-turn", `{"lead_text":"Tenho uma dúvida."}`, true)
	if turn.Code != 200 || !strings.Contains(turn.Body.String(), `"audio_url":"/v1/test/agent-turn/test-a/audio"`) {
		t.Fatalf("turn status=%d body=%s", turn.Code, turn.Body.String())
	}
	audio := ownerRequest(h, http.MethodGet, "/v1/test/agent-turn/test-a/audio", "", true)
	if audio.Code != 200 || audio.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("audio status=%d type=%s", audio.Code, audio.Header().Get("Content-Type"))
	}
	if string(audio.Body.Bytes()[:4]) != "RIFF" || binary.LittleEndian.Uint32(audio.Body.Bytes()[24:28]) != 24000 {
		t.Fatal("WAV does not describe mono PCM at 24kHz")
	}
	missing := ownerRequest(h, http.MethodGet, "/v1/test/agent-turn/no-such-id/audio", "", true)
	if missing.Code != 404 {
		t.Fatalf("missing audio status=%d", missing.Code)
	}
}

func TestTuningValidationBoundsTimeoutAndVoice(t *testing.T) {
	store, err := NewTuningStore(JEVSettings{Model: "m", TimeoutMS: 400}, GeminiSettings{Model: "g", VoiceName: "Kore"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetJEV(JEVSettings{Model: "m", TimeoutMS: 5001}); err == nil {
		t.Fatal("unbounded timeout accepted")
	}
	if err = store.SetGemini(GeminiSettings{Model: "g"}); err == nil {
		t.Fatal("empty voice accepted")
	}
}

func TestEphemeralAudioStoreBoundsAndExpiresEntries(t *testing.T) {
	s := newEphemeralAudioStore()
	if !s.put("sample", []byte{1, 2, 3, 4}) {
		t.Fatal("audio was rejected")
	}
	s.mu.Lock()
	item := s.items["sample"]
	item.expires = time.Now().Add(-time.Second)
	s.items["sample"] = item
	s.mu.Unlock()
	if _, ok := s.get("sample"); ok {
		t.Fatal("expired audio remained readable")
	}
	if s.bytes != 0 {
		t.Fatalf("expired audio bytes=%d", s.bytes)
	}
	if s.put("oversize", make([]byte, (12<<20)+2)) {
		t.Fatal("oversize artifact accepted")
	}
}
