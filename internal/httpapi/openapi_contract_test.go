package httpapi

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCallTranscriptOpenAPILimitMatchesRuntimeCap(t *testing.T) {
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("read OpenAPI spec: %v", err)
	}

	const path = "  /v1/calls/{call_id}/transcript:\n"
	start := strings.Index(string(spec), path)
	if start < 0 {
		t.Fatal("transcript path missing from OpenAPI spec")
	}
	remaining := string(spec)[start+len(path):]
	end := strings.Index(remaining, "  /v1/config/whatsapp:")
	if end < 0 {
		t.Fatal("could not isolate transcript OpenAPI operation")
	}
	operation := remaining[:end]
	var limitParameter string
	for _, line := range strings.Split(operation, "\n") {
		if strings.Contains(line, "name: limit") {
			limitParameter = line
			break
		}
	}
	if limitParameter == "" {
		t.Fatal("transcript limit query parameter missing from OpenAPI spec")
	}
	if strings.Contains(limitParameter, "maximum:") {
		t.Fatalf("query parameter maximum would reject values runtime caps: %s", limitParameter)
	}
	for _, want := range []string{"minimum: 1", "default: 100", "above 500 are capped at 500"} {
		if !strings.Contains(limitParameter, want) {
			t.Errorf("limit contract missing %q: %s", want, limitParameter)
		}
	}
	if !strings.Contains(string(spec), "turns: { type: array, maxItems: 500") {
		t.Error("transcript response must retain the 500-item maximum")
	}
}

func TestCallOpenAPIDocumentsSeparateAIRuntimeStatus(t *testing.T) {
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec)
	for _, fragment := range []string{"required: [call_id, to, status, ai_runtime_status]", "ai_runtime_status: { type: string, enum: [not_started, starting, running, failed, degraded, stopped]", "ai_failure_class: { type: string, enum: [timeout, canceled, receive_failed, provider_api, media_closed, runtime_error, session_ended]", "failed/degraded does not dispatch Hangup"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("Call OpenAPI contract missing %q", fragment)
		}
	}
}

func TestOwnerTuningOpenAPIContract(t *testing.T) {
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec)
	for _, path := range []string{"/api/v1/agent/prompt:", "/api/v1/agent/prompt/versions:", "/api/v1/agent/prompt/versions/{version}/activate:", "/v1/config/jev:", "/v1/config/gemini:", "/v1/test/jev:", "/v1/test/agent-turn:", "/v1/test/agent-turn/{test_id}/audio:", "/v1/runtime/status:"} {
		start := strings.Index(text, "  "+path)
		if start < 0 {
			t.Errorf("OpenAPI path missing %s", path)
			continue
		}
		remaining := text[start+len("  "+path):]
		end := strings.Index(remaining, "\n  /")
		if end < 0 {
			end = strings.Index(remaining, "\ncomponents:")
		}
		if end < 0 {
			end = len(remaining)
		}
		block := remaining[:end]
		operations := strings.Count(block, "operationId:")
		secured := strings.Count(block, "security: [{ BearerAuth: [] }]")
		if operations == 0 || secured != operations {
			t.Errorf("owner operations at %s declare BearerAuth %d times for %d operations", path, secured, operations)
		}
	}
	for _, fragment := range []string{"security: [{ BearerAuth: [] }]", "canonical_default_timeout_ms", "configured_timeout_ms", "decision_guidance", "voice_name", "generation_complete", "turn_complete", "output_transcription", "audio/wav", "type: string, format: binary", "Acceptance:", "Internal Alignment:", "Future Follow-up:", "Cost Objection:", "Security Objection:", "Rejection:", "Opt-out:", "Human Request:", "Clarification:", "Capability Request:", "/openapi.yaml", "GET /v1/runtime/status"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("OpenAPI documentation missing %q", fragment)
		}
	}
	for _, status := range []string{"400", "401", "404", "422", "502", "504"} {
		if !strings.Contains(text, "'"+status+"'") {
			t.Errorf("OpenAPI is missing documented HTTP error %s", status)
		}
	}
	for _, schema := range []string{"PromptDraft:", "PromptVersion:", "PromptVersionList:", "JEVConfigUpdate:", "JEVConfig:", "GeminiConfig:", "DiagnosticLeadRequest:", "AgentTurnRequest:", "JEVTestResult:", "AgentTurnResult:", "RuntimeStatus:"} {
		if !strings.Contains(text, schema) {
			t.Errorf("OpenAPI schema missing %q", schema)
		}
	}
}

func TestFalePacoSIPOpenAPIIsPasswordOnlyAndCanonical(t *testing.T) {
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec)
	start := strings.Index(text, "  /v1/config/sip-trunk:\n")
	if start < 0 {
		t.Fatal("Fale Paco SIP operations are missing from OpenAPI")
	}
	end := strings.Index(text[start:], "  /v1/config/sip-trunk/validate:\n")
	if end < 0 {
		t.Fatal("Fale Paco SIP validation operation is missing from OpenAPI")
	}
	operation := text[start : start+end]
	for _, fragment := range []string{"security: [{ BearerAuth: [] }]", "provider: 'Fale Paco'", "host: '98034.falepaco.com.br'", "port: 5060", "transport: tcp", "username: '100'", "caller_id: '551155200455'", "password_configured: true", "example: { secret: YOUR_SIP_PASSWORD }", "All provider, host, port, transport, username, realm, registrar, proxy, caller ID, and registration fields are server-controlled"} {
		if !strings.Contains(operation, fragment) {
			t.Errorf("Fale Paco SIP OpenAPI missing %q", fragment)
		}
	}
	for _, fragment := range []string{"FalePacoSIPPassword:", "additionalProperties: false", "title: Password", "writeOnly: true"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("Fale Paco write-only password schema missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"SIPConfigRequest", "SIPSafeResponse", "codecs: [\"\"]", "auth: { secret:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("obsolete or unsafe SIP contract remains: %q", forbidden)
		}
	}
	validationStart := strings.Index(text, "  /v1/config/sip-trunk/validate:\n")
	if validationStart < 0 {
		t.Fatal("Fale Paco validation operation is missing from OpenAPI")
	}
	validationEnd := strings.Index(text[validationStart:], "\n  /")
	if validationEnd < 0 {
		validationEnd = len(text) - validationStart
	}
	validation := text[validationStart : validationStart+validationEnd]
	for _, fragment := range []string{"security: [{ BearerAuth: [] }]", "does not send an INVITE", "realm_match", "registration_state", "REGISTERED"} {
		if !strings.Contains(validation, fragment) {
			t.Errorf("Fale Paco validation contract missing %q", fragment)
		}
	}
	embedded, err := os.ReadFile("openapi.yaml")
	if err != nil || string(embedded) != string(spec) {
		t.Fatal("embedded Scalar OpenAPI spec differs from root OpenAPI spec")
	}
}

func TestEmbeddedScalarDocsAreServedLocally(t *testing.T) {
	h := NewRouterWithServicesCallsAndTranscript(nil, unavailableSIPConfigurator{}, nil, nil, nil)
	for _, tc := range []struct{ path, contentType, contains string }{{"/docs", "text/html", "/scalar.js"}, {"/openapi.yaml", "application/yaml", "/v1/config/jev"}, {"/scalar.js", "text/javascript", "Scalar"}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatalf("%s status=%d", tc.path, res.Code)
		}
		if !strings.Contains(res.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(res.Body.String(), tc.contains) {
			t.Fatalf("%s content-type=%q body omitted %q", tc.path, res.Header().Get("Content-Type"), tc.contains)
		}
	}
}
