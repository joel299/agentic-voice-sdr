package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
)

type policyStatusFake struct {
	fakeOutboundCallService
	allowed []string
}

func (s *policyStatusFake) AllowedDestinations() []string { return append([]string{}, s.allowed...) }
func TestOutboundReadinessUsesEffectivePolicyAndOwnerAuth(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "configured"}[configured], func(t *testing.T) {
			svc := &policyStatusFake{}
			if configured {
				svc.allowed = []string{"+5567981340687"}
			}
			store, e := NewTuningStore(JEVSettings{Model: "fixture", TimeoutMS: 400, Description: "fixture", DecisionGuidance: "fixture"}, GeminiSettings{Model: "fixture", VoiceName: "Kore", Description: "fixture", Style: "fixture"})
			if e != nil {
				t.Fatal(e)
			}
			deps := CalibrationServices{Tuning: store, RuntimeStatus: func(context.Context) (map[string]any, error) {
				return map[string]any{"api_ready": true, "baresip_ctrl_ready": true, "baresip_registered": true, "prompt_active": true, "jev_configured": true, "gemini_configured": true}, nil
			}}
			h := NewRouterWithConfigAndCalibration(config.Config{OwnerAPIToken: "token"}, svc, nil, deps)
			unauth := callRequest(t, h, "GET", "/v1/runtime/status", "", false)
			if unauth.Code != 401 {
				t.Fatalf("status not protected: %d", unauth.Code)
			}
			res := callRequest(t, h, "GET", "/v1/runtime/status", "", true)
			var got map[string]any
			if json.Unmarshal(res.Body.Bytes(), &got) != nil || got["outbound_call_ready"] != configured || got["outbound_call_allowlist_configured"] != configured || got["outbound_call_allowed_count"] != float64(len(svc.allowed)) {
				t.Fatalf("status=%s", res.Body)
			}
			ready := callRequest(t, h, "GET", "/readyz", "", false)
			want := 503
			if configured {
				want = 200
			}
			if ready.Code != want {
				t.Fatalf("ready=%d body=%s", ready.Code, ready.Body)
			}
			if svc.starts != 0 {
				t.Fatal("status dispatched call")
			}
			deps.RuntimeStatus = func(context.Context) (map[string]any, error) { return nil, errors.New("secret") }
			h = NewRouterWithConfigAndCalibration(config.Config{OwnerAPIToken: "token"}, svc, nil, deps)
			if callRequest(t, h, "GET", "/readyz", "", false).Code != 503 {
				t.Fatal("status failure ready")
			}
		})
	}
}
func TestEmptyPolicyHTTPUsesServiceUnavailable(t *testing.T) {
	s := &fakeOutboundCallService{err: callservice.ErrDestinationPolicyNotConfigured}
	r := callRequest(t, callTestHandler(s), http.MethodPost, "/v1/calls", `{"to":"+5567981340687"}`, true)
	if r.Code != 503 || r.Body.String() != "{\"error\":\"outbound call policy is not configured\"}\n" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
