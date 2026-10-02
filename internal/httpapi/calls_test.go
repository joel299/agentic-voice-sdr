package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type fakeOutboundCallService struct {
	call    callservice.Call
	err     error
	starts  int
	gets    int
	hangups int
	lastID  string
}

func (s *fakeOutboundCallService) Start(_ context.Context, destination string) (callservice.Call, error) {
	s.starts++
	if s.err != nil {
		return callservice.Call{}, s.err
	}
	call := s.call
	if call.CallID == "" {
		call = callservice.Call{CallID: "call-123", To: destination, Status: callservice.StatusDialing}
	}
	return call, nil
}
func (s *fakeOutboundCallService) Get(id string) (callservice.Call, error) {
	s.gets++
	s.lastID = id
	if s.err != nil {
		return callservice.Call{}, s.err
	}
	if id != "call-123" {
		return callservice.Call{}, callservice.ErrCallNotFound
	}
	failedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return callservice.Call{CallID: id, To: "+5567981340687", Status: callservice.StatusFailed, ProviderCallID: "baresip-9", TerminalReason: "sip_403", AIRuntimeStatus: "failed", AIRuntimeStage: "input_transcription_receive", AIFailureClass: "provider_transport", AIFailureAt: &failedAt}, nil
}
func (s *fakeOutboundCallService) Hangup(_ context.Context, id string) (callservice.Call, error) {
	s.hangups++
	s.lastID = id
	if s.err != nil {
		return callservice.Call{}, s.err
	}
	if id != "call-123" {
		return callservice.Call{}, callservice.ErrCallNotFound
	}
	return callservice.Call{CallID: id, Status: callservice.StatusConnected}, nil
}

func callTestHandler(service OutboundCallService) http.Handler {
	authorizer, _ := NewStaticBearerAuthorizer("token")
	return NewRouterWithServicesAndCalls(whatsapp.NewService(nil, nil), unavailableSIPConfigurator{}, service, authorizer)
}

func callRequest(t *testing.T, handler http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer token")
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestOutboundCallRoutesRequireOwnerAuthorization(t *testing.T) {
	service := &fakeOutboundCallService{}
	handler := callTestHandler(service)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/calls", `{"to":"+5567981340687"}`},
		{http.MethodGet, "/v1/calls/call-123", ""},
		{http.MethodPost, "/v1/calls/call-123/hangup", "{}"},
	} {
		res := callRequest(t, handler, request.method, request.path, request.body, false)
		if res.Code != http.StatusUnauthorized || res.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("%s %s: status=%d body=%s", request.method, request.path, res.Code, res.Body)
		}
	}
	invalid := httptest.NewRequest(http.MethodPost, "/v1/calls", strings.NewReader(`{"to":"+5567981340687"}`))
	invalid.Header.Set("Authorization", "Bearer invalid")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status=%d body=%s", invalidResponse.Code, invalidResponse.Body)
	}
	if service.starts != 0 || service.gets != 0 || service.hangups != 0 {
		t.Fatalf("unauthorized request reached service: %+v", service)
	}
}

func TestCreateCallAcceptedAndMakesOneRequest(t *testing.T) {
	service := &fakeOutboundCallService{}
	res := callRequest(t, callTestHandler(service), http.MethodPost, "/v1/calls", `{"to":"+5567981340687"}`, true)
	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"call_id":"call-123"`) || !strings.Contains(res.Body.String(), `"status":"dialing"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	if service.starts != 1 {
		t.Fatalf("Start calls=%d, want 1", service.starts)
	}
}

func TestCallRoutesGetUnknownHangupAndSafeErrors(t *testing.T) {
	service := &fakeOutboundCallService{}
	handler := callTestHandler(service)
	res := callRequest(t, handler, http.MethodGet, "/v1/calls/call-123", "", true)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"provider_call_id":"baresip-9"`) || !strings.Contains(res.Body.String(), `"terminal_reason":"sip_403"`) || !strings.Contains(res.Body.String(), `"ai_runtime_status":"failed"`) || !strings.Contains(res.Body.String(), `"ai_runtime_stage":"input_transcription_receive"`) || !strings.Contains(res.Body.String(), `"ai_failure_class":"provider_transport"`) || !strings.Contains(res.Body.String(), `"ai_failure_at":"2026-10-01T12:00:00Z"`) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	res = callRequest(t, handler, http.MethodGet, "/v1/calls/unknown", "", true)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown status=%d body=%s", res.Code, res.Body)
	}
	res = callRequest(t, handler, http.MethodPost, "/v1/calls/call-123/hangup", "{}", true)
	if res.Code != http.StatusAccepted || service.hangups != 1 || service.lastID != "call-123" {
		t.Fatalf("hangup status=%d service=%+v body=%s", res.Code, service, res.Body)
	}
	service.err = errors.New("Authorization: secret SIP password")
	res = callRequest(t, handler, http.MethodPost, "/v1/calls", `{"to":"+5567981340687"}`, true)
	if res.Code != http.StatusBadGateway || strings.Contains(res.Body.String(), "secret") || strings.Contains(res.Body.String(), "Authorization") {
		t.Fatalf("provider error leaked: status=%d body=%s", res.Code, res.Body)
	}
}

func TestCallRouteMapsDestinationAndRegistrationErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{callservice.ErrInvalidDestination, http.StatusBadRequest},
		{callservice.ErrDestinationDenied, http.StatusForbidden},
		{callservice.ErrDestinationPolicyNotConfigured, http.StatusServiceUnavailable},
		{callservice.ErrNotRegistered, http.StatusConflict},
		{callservice.ErrCallActive, http.StatusConflict},
	} {
		service := &fakeOutboundCallService{err: tc.err}
		res := callRequest(t, callTestHandler(service), http.MethodPost, "/v1/calls", `{"to":"+5567981340687"}`, true)
		if res.Code != tc.want {
			t.Errorf("error %v mapped to %d, want %d: %s", tc.err, res.Code, tc.want, res.Body)
		}
	}
}
