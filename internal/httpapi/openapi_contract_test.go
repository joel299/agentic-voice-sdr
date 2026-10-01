package httpapi

import (
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
