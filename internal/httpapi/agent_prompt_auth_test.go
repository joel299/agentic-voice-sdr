package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
)

type ownerAuthorizerFunc func(*http.Request) bool

func (f ownerAuthorizerFunc) Authorize(r *http.Request) bool { return f(r) }

func protectedPromptHandler(t *testing.T, manager *agentPromptManagerFake, authorizer OwnerAuthorizer) http.Handler {
	t.Helper()
	handler, err := NewProtectedAgentPromptHandler(manager, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestNewProtectedAgentPromptHandlerFailsClosedForNilDependencies(t *testing.T) {
	authorizer := ownerAuthorizerFunc(func(*http.Request) bool { return true })
	manager := &agentPromptManagerFake{}
	if _, err := NewProtectedAgentPromptHandler(nil, authorizer); err == nil {
		t.Fatal("nil manager should fail")
	}
	if _, err := NewProtectedAgentPromptHandler(manager, nil); err == nil {
		t.Fatal("nil authorizer should fail")
	}
}

func TestProtectedAgentPromptRejectsBeforeManagerForEveryEndpoint(t *testing.T) {
	manager := &agentPromptManagerFake{}
	handler := protectedPromptHandler(t, manager, ownerAuthorizerFunc(func(*http.Request) bool { return false }))
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/agent/prompt", ""},
		{http.MethodPut, "/api/v1/agent/prompt", `{"name":"x","prompt":"HIGHLY-SENSITIVE-PROMPT-CONTENT"}`},
		{http.MethodGet, "/api/v1/agent/prompt/versions", ""},
		{http.MethodPost, "/api/v1/agent/prompt/versions/2/activate", ""},
	}
	for _, tc := range requests {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized || res.Header().Get("WWW-Authenticate") != "Bearer" || res.Body.String() != `{"error":"unauthorized"}`+"\n" {

			t.Fatalf("%s %s: status=%d challenge=%q body=%q", tc.method, tc.path, res.Code, res.Header().Get("WWW-Authenticate"), res.Body.String())
		}
		if strings.Contains(res.Body.String(), "HIGHLY-SENSITIVE-PROMPT-CONTENT") {
			t.Fatal("rejected response leaked prompt")
		}
	}
	if manager.activeCalls != 0 || manager.createCalls != 0 || manager.listCalls != 0 || manager.activateCalls != 0 {
		t.Fatalf("unauthorized request called manager: active=%d create=%d list=%d activate=%d", manager.activeCalls, manager.createCalls, manager.listCalls, manager.activateCalls)
	}
}

func TestProtectedAgentPromptAuthorizedRequestsReuseExistingHandler(t *testing.T) {
	manager := &agentPromptManagerFake{active: newHTTPPrompt(t, 3, "SDR", "body", true, time.Date(2026, 1, 3, 13, 0, 0, 0, time.UTC))}
	handler := protectedPromptHandler(t, manager, ownerAuthorizerFunc(func(*http.Request) bool { return true }))

	for _, tc := range []struct {
		method string
		path   string
		body   string
		check  func(*testing.T)
	}{
		{http.MethodGet, "/api/v1/agent/prompt", "", func(t *testing.T) {
			if manager.activeCalls != 1 {
				t.Fatalf("active calls=%d", manager.activeCalls)
			}
		}},
		{http.MethodPut, "/api/v1/agent/prompt", `{"name":"new","prompt":"new body"}`, func(t *testing.T) {
			if manager.createCalls != 1 || manager.activateCalls != 0 || manager.lastDraft.Name != "new" || manager.lastDraft.Content != "new body" {
				t.Fatalf("create=%d activate=%d draft=%+v", manager.createCalls, manager.activateCalls, manager.lastDraft)
			}
		}},
		{http.MethodGet, "/api/v1/agent/prompt/versions", "", func(t *testing.T) {
			if manager.listCalls != 1 {
				t.Fatalf("list calls=%d", manager.listCalls)
			}
		}},
		{http.MethodPost, "/api/v1/agent/prompt/versions/9/activate", "", func(t *testing.T) {
			if manager.activateCalls != 1 || manager.lastVersion != 9 {
				t.Fatalf("activate=%d version=%d", manager.activateCalls, manager.lastVersion)
			}
		}},
	} {
		res := promptRequest(t, handler, tc.method, tc.path, tc.body)
		if res.Code != http.StatusOK && !(tc.method == http.MethodPut && res.Code == http.StatusCreated) {
			t.Fatalf("%s %s: status=%d body=%s", tc.method, tc.path, res.Code, res.Body)
		}
		tc.check(t)
	}
}

func TestProtectedAgentPromptPropagatesRequestContextToAuthorizerAndManager(t *testing.T) {
	manager := &agentPromptManagerFake{}
	const key = "owner-context"
	want := "context-value"
	var authContext context.Context
	authorizer := ownerAuthorizerFunc(func(r *http.Request) bool {
		authContext = r.Context()
		return true
	})
	handler := protectedPromptHandler(t, manager, authorizer)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/prompt", nil)
	req = req.WithContext(context.WithValue(req.Context(), key, want))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || authContext == nil || authContext.Value(key) != want || manager.lastContext == nil || manager.lastContext.Value(key) != want {
		t.Fatalf("context not propagated: auth=%v manager=%v", authContext, manager.lastContext)
	}
}

func TestProtectedAgentPromptPreservesExistingErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{agentprompt.ErrNoActivePrompt, http.StatusNotFound},
		{agentprompt.ErrPromptNotFound, http.StatusNotFound},
		{agentprompt.ErrInvalidPrompt, http.StatusBadRequest},
		{errors.New("internal secret"), http.StatusInternalServerError},
	} {
		manager := &agentPromptManagerFake{err: tc.err, createErr: tc.err, activateErr: tc.err}
		handler := protectedPromptHandler(t, manager, ownerAuthorizerFunc(func(*http.Request) bool { return true }))
		res := promptRequest(t, handler, http.MethodGet, "/api/v1/agent/prompt", "")
		if res.Code != tc.code || strings.Contains(res.Body.String(), "internal secret") {
			t.Fatalf("status=%d body=%s", res.Code, res.Body)
		}
	}
}

func TestStaticBearerAuthorizer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		allow  bool
	}{
		{"correct", "Bearer owner-secret", true},
		{"lowercase scheme", "bearer owner-secret", true},
		{"uppercase scheme", "BEARER owner-secret", true},
		{"mixed case scheme", "BeArEr owner-secret", true},
		{"wrong", "Bearer wrong", false},
		{"missing", "", false},
		{"wrong scheme", "Basic owner-secret", false},
		{"empty bearer", "Bearer", false},
		{"blank bearer", "Bearer ", false},
		{"multiple values", "Bearer owner-secret extra", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorizer, err := NewStaticBearerAuthorizer("owner-secret")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.header)
			if got := authorizer.Authorize(req); got != tc.allow {
				t.Fatalf("authorized=%v, want %v", got, tc.allow)
			}
		})
	}
	for _, expected := range []string{"", " ", "\t", "\n", " owner-secret", "owner-secret ", "owner secret", "owner-secret\n", "owner-secret\t"} {
		if _, err := NewStaticBearerAuthorizer(expected); err == nil {
			t.Fatalf("expected empty token %q to fail", expected)
		}
	}
}
