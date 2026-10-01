package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/transcriptquery"
	"github.com/joel299/agentic-voice-sdr/internal/whatsapp"
)

type fakeTranscriptReader struct {
	result transcriptquery.Transcript
	err    error
	callID string
	limit  int
	calls  int
}

func (r *fakeTranscriptReader) Get(_ context.Context, callID string, limit int) (transcriptquery.Transcript, error) {
	r.calls++
	r.callID, r.limit = callID, limit
	return r.result, r.err
}

func transcriptTestHandler(reader CallTranscriptReader) http.Handler {
	authorizer, _ := NewStaticBearerAuthorizer("owner-token")
	return NewRouterWithServicesCallsAndTranscript(whatsapp.NewService(nil, nil), unavailableSIPConfigurator{}, nil, reader, authorizer)
}

func transcriptRequest(handler http.Handler, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestCallTranscriptRequiresOwnerBearerAuth(t *testing.T) {
	reader := &fakeTranscriptReader{}
	handler := transcriptTestHandler(reader)
	for _, authorization := range []string{"", "Bearer invalid"} {
		res := transcriptRequest(handler, "/v1/calls/call-1/transcript", authorization)
		if res.Code != http.StatusUnauthorized || res.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("authorization %q status=%d body=%s", authorization, res.Code, res.Body)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("unauthorized requests reached query service %d times", reader.calls)
	}
}

func TestCallTranscriptEmptyActiveAndCompletedResponsesAreSafe(t *testing.T) {
	now := time.Date(2026, 9, 30, 2, 3, 4, 0, time.UTC)
	reader := &fakeTranscriptReader{result: transcriptquery.Transcript{CallID: "call-1", Status: "connected", Turns: []transcriptquery.Turn{}}}
	handler := transcriptTestHandler(reader)
	res := transcriptRequest(handler, "/v1/calls/call-1/transcript", "Bearer owner-token")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"connected"`) || !strings.Contains(res.Body.String(), `"turns":[]`) {
		t.Fatalf("empty active response status=%d body=%s", res.Code, res.Body)
	}
	if reader.limit != transcriptquery.DefaultLimit {
		t.Fatalf("default limit=%d", reader.limit)
	}
	reader.result = transcriptquery.Transcript{CallID: "call-1", Status: "completed", Turns: []transcriptquery.Turn{
		{Sequence: 1, Role: "lead", Text: "Olá", CreatedAt: now},
		{Sequence: 2, Role: "agent", Text: "Bem-vindo", CreatedAt: now},
	}}
	res = transcriptRequest(handler, "/v1/calls/call-1/transcript?limit=4", "Bearer owner-token")
	body := res.Body.String()
	for _, want := range []string{`"call_id":"call-1"`, `"status":"completed"`, `"sequence":1`, `"role":"lead"`, `"sequence":2`, `"role":"agent"`, now.Format(time.RFC3339)} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %q: %s", want, body)
		}
	}
	for _, forbidden := range []string{"provider_call_id", "idempotency_key", "event_id", "Authorization", "provider_call_id", "database password"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	if reader.callID != "call-1" || reader.limit != 4 {
		t.Fatalf("query received callID=%q limit=%d", reader.callID, reader.limit)
	}
}

func TestCallTranscriptMapsNotFoundAndMasksQueryErrors(t *testing.T) {
	reader := &fakeTranscriptReader{err: transcriptquery.ErrCallNotFound}
	handler := transcriptTestHandler(reader)
	res := transcriptRequest(handler, "/v1/calls/missing/transcript", "Bearer owner-token")
	if res.Code != http.StatusNotFound || strings.Contains(res.Body.String(), "database") {
		t.Fatalf("not found status=%d body=%s", res.Code, res.Body)
	}
	reader.err = transcriptquery.ErrInvalidCall
	res = transcriptRequest(handler, "/v1/calls/call-1/transcript", "Bearer owner-token")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid call id status=%d body=%s", res.Code, res.Body)
	}
	reader.err = errors.New("database password: top-secret")
	res = transcriptRequest(handler, "/v1/calls/call-1/transcript", "Bearer owner-token")
	if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "top-secret") || strings.Contains(res.Body.String(), "database") {
		t.Fatalf("internal error leaked status=%d body=%s", res.Code, res.Body)
	}
}

func TestCallTranscriptCapsLimitAndRejectsInvalidValues(t *testing.T) {
	reader := &fakeTranscriptReader{result: transcriptquery.Transcript{CallID: "call-1", Status: "failed", Turns: []transcriptquery.Turn{}}}
	handler := transcriptTestHandler(reader)
	res := transcriptRequest(handler, "/v1/calls/call-1/transcript?limit=900", "Bearer owner-token")
	if res.Code != http.StatusOK || reader.limit != transcriptquery.MaxLimit {
		t.Fatalf("over-max limit status=%d received=%d body=%s", res.Code, reader.limit, res.Body)
	}
	for _, invalid := range []string{"abc", "0", "-2"} {
		res := transcriptRequest(handler, "/v1/calls/call-1/transcript?limit="+invalid, "Bearer owner-token")
		if res.Code != http.StatusBadRequest {
			t.Errorf("limit %q status=%d body=%s", invalid, res.Code, res.Body)
		}
	}
}
