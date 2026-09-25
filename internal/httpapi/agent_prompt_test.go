package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
)

type agentPromptManagerFake struct {
	active        agentprompt.PromptVersion
	versions      []agentprompt.PromptVersion
	err           error
	createErr     error
	activateErr   error
	createCalls   int
	listCalls     int
	activeCalls   int
	activateCalls int
	lastDraft     agentprompt.PromptDraft
	lastVersion   int
	lastContext   context.Context
}

func (m *agentPromptManagerFake) GetActive(ctx context.Context) (agentprompt.PromptVersion, error) {
	m.activeCalls++
	m.lastContext = ctx
	if m.err != nil {
		return agentprompt.PromptVersion{}, m.err
	}
	return m.active, nil
}
func (m *agentPromptManagerFake) ListVersions(ctx context.Context) ([]agentprompt.PromptVersion, error) {
	m.listCalls++
	m.lastContext = ctx
	if m.err != nil {
		return nil, m.err
	}
	return m.versions, nil
}
func (m *agentPromptManagerFake) ActivateVersion(ctx context.Context, version int) (agentprompt.PromptVersion, error) {
	m.activateCalls++
	m.lastContext = ctx
	m.lastVersion = version
	if m.activateErr != nil {
		return agentprompt.PromptVersion{}, m.activateErr
	}
	return m.active, nil
}
func (m *agentPromptManagerFake) CreateAndActivate(ctx context.Context, draft agentprompt.PromptDraft) (agentprompt.PromptVersion, error) {
	m.createCalls++
	m.lastContext = ctx
	m.lastDraft = draft
	if m.createErr != nil {
		return agentprompt.PromptVersion{}, m.createErr
	}
	return m.active, nil
}

func newHTTPPrompt(t *testing.T, version int, name, content string, active bool, activatedAt time.Time) agentprompt.PromptVersion {
	t.Helper()
	prompt, err := agentprompt.NewPromptVersion(version, name, content, active, time.Date(2026, 1, version, 12, 0, 0, 0, time.UTC), activatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return prompt
}

func promptHandler(t *testing.T, manager *agentPromptManagerFake) http.Handler {
	t.Helper()
	handler, err := NewAgentPromptHandler(manager)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func promptRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestNewAgentPromptHandlerRejectsNilManager(t *testing.T) {
	if _, err := NewAgentPromptHandler(nil); err == nil {
		t.Fatal("nil manager should fail")
	}
}

func TestAgentPromptGetActive(t *testing.T) {
	m := &agentPromptManagerFake{active: newHTTPPrompt(t, 3, "SDR", "prompt body", true, time.Date(2026, 1, 3, 13, 0, 0, 0, time.UTC))}
	res := promptRequest(t, promptHandler(t, m), http.MethodGet, "/api/v1/agent/prompt", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	var body agentPromptResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != 3 || body.Name != "SDR" || body.Prompt != "prompt body" || !body.IsActive || body.ActivatedAt == nil {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.CreatedAt != "2026-01-03T12:00:00Z" || *body.ActivatedAt != "2026-01-03T13:00:00Z" {
		t.Fatalf("unexpected timestamps: %+v", body)
	}
}

func TestAgentPromptGetActiveErrorsAreSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"none", agentprompt.ErrNoActivePrompt, http.StatusNotFound, "agent prompt is not configured"},
		{"internal", errors.New("secret prompt"), http.StatusInternalServerError, "agent prompt request failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &agentPromptManagerFake{err: tc.err}
			res := promptRequest(t, promptHandler(t, m), http.MethodGet, "/api/v1/agent/prompt", "")
			if res.Code != tc.code || strings.Contains(res.Body.String(), "secret prompt") || !strings.Contains(res.Body.String(), tc.msg) {
				t.Fatalf("status=%d body=%s", res.Code, res.Body)
			}
		})
	}
}

func TestAgentPromptPutCallsCreateAndActivateOnceAndMapsFields(t *testing.T) {
	m := &agentPromptManagerFake{active: newHTTPPrompt(t, 4, "SDR", "body", true, time.Date(2026, 1, 4, 13, 0, 0, 0, time.UTC))}
	res := promptRequest(t, promptHandler(t, m), http.MethodPut, "/api/v1/agent/prompt", "{\"name\":\" SDR \",\"prompt\":\" body \"}")
	if res.Code != http.StatusCreated || m.createCalls != 1 || m.activateCalls != 0 || m.lastDraft.Name != " SDR " || m.lastDraft.Content != " body " {
		t.Fatalf("status=%d create=%d activate=%d draft=%+v", res.Code, m.createCalls, m.activateCalls, m.lastDraft)
	}
}

func TestAgentPromptPutRejectsUnsafeOrInvalidBodies(t *testing.T) {
	bodies := []string{
		"{\"name\":\"x\"",
		"{\"name\":\"x\",\"prompt\":\"y\",\"extra\":\"nope\"}",
		"{\"name\":\"x\",\"prompt\":\"y\"}{\"name\":\"z\",\"prompt\":\"w\"}",
	}
	for _, body := range bodies {
		m := &agentPromptManagerFake{}
		res := promptRequest(t, promptHandler(t, m), http.MethodPut, "/api/v1/agent/prompt", body)
		if res.Code != http.StatusBadRequest || m.createCalls != 0 {
			t.Fatalf("status=%d create=%d body=%s", res.Code, m.createCalls, res.Body)
		}
	}
}

func TestAgentPromptPutErrorsAreSafe(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		msg  string
	}{
		{agentprompt.ErrInvalidPrompt, http.StatusBadRequest, "invalid agent prompt"},
		{errors.New("secret create error"), http.StatusInternalServerError, "agent prompt request failed"},
	} {
		m := &agentPromptManagerFake{createErr: tc.err}
		res := promptRequest(t, promptHandler(t, m), http.MethodPut, "/api/v1/agent/prompt", "{\"name\":\"x\",\"prompt\":\"y\"}")
		if res.Code != tc.code || strings.Contains(res.Body.String(), "secret create error") || !strings.Contains(res.Body.String(), tc.msg) {
			t.Fatalf("status=%d body=%s", res.Code, res.Body)
		}
	}
}

func TestAgentPromptListPreservesOrderAndEmptyArray(t *testing.T) {
	versions := []agentprompt.PromptVersion{
		newHTTPPrompt(t, 1, "one", "first", false, time.Time{}),
		newHTTPPrompt(t, 2, "two", "second", false, time.Date(2026, 1, 2, 13, 0, 0, 0, time.UTC)),
	}
	for _, input := range [][]agentprompt.PromptVersion{versions, {}} {
		m := &agentPromptManagerFake{versions: input}
		res := promptRequest(t, promptHandler(t, m), http.MethodGet, "/api/v1/agent/prompt/versions", "")
		if res.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.Code, res.Body)
		}
		var body map[string][]agentPromptResponse
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got := body["versions"]
		if len(got) != len(input) || (len(input) == 2 && got[0].Version != 1) {
			t.Fatalf("ordering/body=%+v raw=%s", got, res.Body)
		}
		if len(input) == 0 && !strings.Contains(res.Body.String(), "\"versions\":[]") {
			t.Fatalf("empty list was not []: %s", res.Body)
		}
	}
}

func TestAgentPromptActivateAndInvalidVersionCalls(t *testing.T) {
	m := &agentPromptManagerFake{active: newHTTPPrompt(t, 7, "seven", "content", true, time.Date(2026, 1, 7, 13, 0, 0, 0, time.UTC))}
	res := promptRequest(t, promptHandler(t, m), http.MethodPost, "/api/v1/agent/prompt/versions/7/activate", "")
	if res.Code != http.StatusOK || m.activateCalls != 1 || m.lastVersion != 7 {
		t.Fatalf("valid activation status=%d calls=%d version=%d", res.Code, m.activateCalls, m.lastVersion)
	}
	for _, version := range []string{"0", "-1", "bad", "999999999999999999999999999999"} {
		m := &agentPromptManagerFake{}
		res := promptRequest(t, promptHandler(t, m), http.MethodPost, "/api/v1/agent/prompt/versions/"+version+"/activate", "")
		if res.Code != http.StatusBadRequest || m.activateCalls != 0 {
			t.Fatalf("version=%s status=%d calls=%d", version, res.Code, m.activateCalls)
		}
	}
}

func TestAgentPromptActivateErrorsAndDTONull(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{agentprompt.ErrPromptNotFound, http.StatusNotFound},
		{agentprompt.ErrInvalidPrompt, http.StatusBadRequest},
		{errors.New("secret activate error"), http.StatusInternalServerError},
	} {
		m := &agentPromptManagerFake{activateErr: tc.err}
		res := promptRequest(t, promptHandler(t, m), http.MethodPost, "/api/v1/agent/prompt/versions/2/activate", "")
		if res.Code != tc.code || strings.Contains(res.Body.String(), "secret activate error") {
			t.Fatalf("status=%d body=%s", res.Code, res.Body)
		}
	}
	m := &agentPromptManagerFake{versions: []agentprompt.PromptVersion{newHTTPPrompt(t, 1, "one", "first", false, time.Time{})}}
	res := promptRequest(t, promptHandler(t, m), http.MethodGet, "/api/v1/agent/prompt/versions", "")
	if !strings.Contains(res.Body.String(), "\"activated_at\":null") {
		t.Fatalf("never active DTO did not use null: %s", res.Body)
	}
}

func TestAgentPromptPropagatesRequestContext(t *testing.T) {
	m := &agentPromptManagerFake{}
	handler := promptHandler(t, m)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/prompt", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if m.lastContext == nil || m.lastContext.Err() != context.Canceled {
		t.Fatalf("context was not propagated: %#v", m.lastContext)
	}
}
