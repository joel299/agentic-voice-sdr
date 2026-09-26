package sessionprompt

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

var zeroTime time.Time

var (
	errSecret = "postgres://username:password@db.invalid/private?token=secret"
)

type sourceFunc func(context.Context) (agentprompt.PromptSnapshot, error)

func (f sourceFunc) SnapshotActive(ctx context.Context) (agentprompt.PromptSnapshot, error) {
	return f(ctx)
}

func snapshot(version int, name, content string) agentprompt.PromptSnapshot {
	prompt, err := agentprompt.NewPromptVersion(version, name, content, false, zeroTime, zeroTime)
	if err != nil {
		panic(err)
	}
	return prompt.Snapshot()
}

func TestNewBuilderRejectsMissingDependenciesAndBlankCore(t *testing.T) {
	base := geminilive.Config{}
	validSource := sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) { return snapshot(1, "name", "content"), nil })
	if _, err := NewBuilder(base, "core", nil); !errors.Is(err, ErrInvalidBuilder) {
		t.Fatalf("nil source error = %v", err)
	}
	for _, core := range []string{"", " \t\n"} {
		if _, err := NewBuilder(base, core, validSource); !errors.Is(err, ErrInvalidBuilder) {
			t.Fatalf("blank core %q error = %v", core, err)
		}
	}
}

func TestBuildCallsSourceExactlyOnceAndPreservesBaseConfig(t *testing.T) {
	var calls int
	tools := []geminilive.ToolDefinition{{FunctionDeclarations: []geminilive.FunctionDeclaration{{Name: "schedule", Parameters: map[string]any{"nested": map[string]any{"enabled": true}}}}}}
	modalities := []string{"AUDIO", "TEXT"}
	base := geminilive.Config{APIKey: "api-key-secret", Model: "model-7", Endpoint: "wss://host.invalid/path?key=secret", SystemInstruction: "old arbitrary instruction", Tools: tools, ResponseModalities: modalities}
	builder, err := NewBuilder(base, "fixed safety core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		calls++
		return snapshot(7, "SDR principal", "Fale com acolhimento."), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	config, got, err := builder.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("SnapshotActive calls = %d, want 1", calls)
	}
	if config.APIKey != base.APIKey || config.Model != base.Model || config.Endpoint != base.Endpoint || !reflect.DeepEqual(config.Tools, base.Tools) || !reflect.DeepEqual(config.ResponseModalities, base.ResponseModalities) {
		t.Fatal("Build did not preserve base Gemini config")
	}
	wantInstruction := "[IMMUTABLE SYSTEM CORE]\nfixed safety core\n\n[ACTIVE EDITABLE AGENT PROMPT v7: SDR principal]\nFale com acolhimento."
	if config.SystemInstruction != wantInstruction {
		t.Fatalf("SystemInstruction = %q, want deterministic composition %q", config.SystemInstruction, wantInstruction)
	}
	if got.Version() != 7 || got.Name() != "SDR principal" || got.Content() != "Fale com acolhimento." {
		t.Fatal("returned snapshot differs from composed snapshot")
	}
	if strings.Contains(config.SystemInstruction, "old arbitrary instruction") {
		t.Fatal("existing SystemInstruction must be replaced, not appended")
	}
}

func TestBuildFreezesSnapshotPerSessionAcrossActivation(t *testing.T) {
	var mu sync.RWMutex
	active := snapshot(1, "Persona", "prompt-v1")
	calls := 0
	source := sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return active, nil
	})
	builder, err := NewBuilder(geminilive.Config{SystemInstruction: "stale"}, "immutable core", source)
	if err != nil {
		t.Fatal(err)
	}
	configA, snapA, err := builder.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	configAOriginal := configA.SystemInstruction
	mu.Lock()
	active = snapshot(2, "Persona v2", "prompt-v2")
	mu.Unlock()
	if configA.SystemInstruction != configAOriginal || snapA.Version() != 1 || snapA.Content() != "prompt-v1" {
		t.Fatal("existing session A changed after activation")
	}
	configB, snapB, err := builder.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapB.Version() != 2 || !strings.Contains(configB.SystemInstruction, "prompt-v2") {
		t.Fatal("new session B did not capture v2")
	}
	if configA.SystemInstruction != configAOriginal || strings.Contains(configA.SystemInstruction, "prompt-v2") {
		t.Fatal("session A instruction was mutated")
	}
}

func TestBuildPropagatesNoActiveAndSanitizesSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sourceErr error
		want      error
	}{{"no active", agentprompt.ErrNoActivePrompt, agentprompt.ErrNoActivePrompt}, {"unexpected", fmt.Errorf("%s prompt body", errSecret), ErrSnapshotSource}} {
		t.Run(tc.name, func(t *testing.T) {
			builder, err := NewBuilder(geminilive.Config{APIKey: "api-key-secret", Endpoint: errSecret}, "fixed core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
				return agentprompt.PromptSnapshot{}, tc.sourceErr
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = builder.Build(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("Build error %v does not wrap %v", err, tc.want)
			}
			for _, secret := range []string{"api-key-secret", errSecret, "prompt body"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Build error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestBuildRejectsNilAndCanceledContext(t *testing.T) {
	builder, err := NewBuilder(geminilive.Config{}, "core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) { return snapshot(1, "n", "p"), nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := builder.Build(nil); !errors.Is(err, ErrInvalidBuildContext) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	builder, _ = NewBuilder(geminilive.Config{}, "core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) { calls++; return snapshot(1, "n", "p"), nil }))
	if _, _, err := builder.Build(ctx); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled build err=%v calls=%d", err, calls)
	}
	wrappedCancel, _ := NewBuilder(geminilive.Config{}, "core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		return agentprompt.PromptSnapshot{}, fmt.Errorf("%s: %w", errSecret, context.Canceled)
	}))
	_, _, err = wrappedCancel.Build(context.Background())
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), errSecret) {
		t.Fatalf("wrapped context error was not sanitized: %v", err)
	}
}

func TestBuildsAreConcurrentAndReturnIndependentConfigs(t *testing.T) {
	const n = 32
	var calls int
	var mu sync.Mutex
	builder, err := NewBuilder(geminilive.Config{Tools: []geminilive.ToolDefinition{{FunctionDeclarations: []geminilive.FunctionDeclaration{{Name: "f", Parameters: map[string]any{"flags": []any{"a", "b"}}}}}}, ResponseModalities: []string{"AUDIO"}}, "core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		mu.Lock()
		calls++
		id := calls
		mu.Unlock()
		return snapshot(id, "session", fmt.Sprintf("prompt-%d", id)), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	configs := make([]geminilive.Config, n)
	versions := make([]int, n)
	var wg sync.WaitGroup
	for i := range configs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var snap agentprompt.PromptSnapshot
			var buildErr error
			configs[i], snap, buildErr = builder.Build(context.Background())
			if buildErr != nil {
				t.Errorf("Build: %v", buildErr)
				return
			}
			versions[i] = snap.Version()
		}(i)
	}
	wg.Wait()
	if calls != n {
		t.Fatalf("SnapshotActive calls = %d, want %d", calls, n)
	}
	seen := make(map[int]bool, n)
	for i, config := range configs {
		if seen[versions[i]] || !strings.Contains(config.SystemInstruction, fmt.Sprintf("prompt-%d", versions[i])) {
			t.Fatalf("build %d has mismatched/duplicate snapshot version %d", i, versions[i])
		}
		seen[versions[i]] = true
	}
	configs[0].ResponseModalities[0] = "MUTATED"
	configs[0].Tools[0].FunctionDeclarations[0].Parameters["flags"].([]any)[0] = "MUTATED"
	if configs[1].ResponseModalities[0] != "AUDIO" || configs[1].Tools[0].FunctionDeclarations[0].Parameters["flags"].([]any)[0] != "a" {
		t.Fatal("concurrent session configs share mutable slices/maps")
	}
}

func TestBuildRejectsOversizedInstructionWithoutTruncating(t *testing.T) {
	called := 0
	builder, err := NewBuilder(geminilive.Config{}, strings.Repeat("c", MaxSystemInstructionBytes), sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		called++
		return snapshot(1, "persona", "editable prompt"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := builder.Build(context.Background())
	if !errors.Is(err, ErrInstructionTooLarge) || called != 1 || config.SystemInstruction != "" {
		t.Fatalf("oversized instruction: config=%q err=%v source calls=%d", config.SystemInstruction, err, called)
	}
}

func TestBuilderDeepClonesTypedToolConfigAndSessionConfigs(t *testing.T) {
	base := geminilive.Config{Tools: []geminilive.ToolDefinition{{FunctionDeclarations: []geminilive.FunctionDeclaration{{Name: "typed", Parameters: map[string]any{
		"metadata": map[string]string{"mode": "safe"},
		"rules":    []map[string]any{{"name": "a"}},
		"matrix":   [][]string{{"north", "south"}},
		"wrapped":  any([1]map[string]string{{"state": "fixed"}}),
	}}}}}}
	builder, err := NewBuilder(base, "immutable core", sourceFunc(func(context.Context) (agentprompt.PromptSnapshot, error) {
		return snapshot(1, "persona", "editable"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	// The builder owns the original config value captured at construction.
	base.Tools[0].FunctionDeclarations[0].Parameters["metadata"].(map[string]string)["mode"] = "base-mutated"
	base.Tools[0].FunctionDeclarations[0].Parameters["rules"].([]map[string]any)[0]["name"] = "base-mutated"

	sessionA, _, err := builder.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	paramsA := sessionA.Tools[0].FunctionDeclarations[0].Parameters
	if paramsA["metadata"].(map[string]string)["mode"] != "safe" || paramsA["rules"].([]map[string]any)[0]["name"] != "a" {
		t.Fatal("mutating the caller's base config changed the builder's captured copy")
	}
	if got := paramsA["matrix"].([][]string)[0][0]; got != "north" {
		t.Fatalf("typed nested slice was not preserved: %q", got)
	}
	if got := paramsA["wrapped"].([1]map[string]string)[0]["state"]; got != "fixed" {
		t.Fatalf("array nested in interface was not preserved: %q", got)
	}

	paramsA["metadata"].(map[string]string)["mode"] = "session-a-mutated"
	paramsA["rules"].([]map[string]any)[0]["name"] = "session-a-mutated"
	paramsA["matrix"].([][]string)[0][0] = "session-a-mutated"
	paramsA["wrapped"].([1]map[string]string)[0]["state"] = "session-a-mutated"

	sessionB, _, err := builder.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	paramsB := sessionB.Tools[0].FunctionDeclarations[0].Parameters
	if paramsB["metadata"].(map[string]string)["mode"] != "safe" {
		t.Fatal("map[string]string from Session A was shared with Session B")
	}
	if paramsB["rules"].([]map[string]any)[0]["name"] != "a" {
		t.Fatal("[]map[string]any from Session A was shared with Session B")
	}
	if paramsB["matrix"].([][]string)[0][0] != "north" {
		t.Fatal("[][]string from Session A was shared with Session B")
	}
	if paramsB["wrapped"].([1]map[string]string)[0]["state"] != "fixed" {
		t.Fatal("array/interface containers from Session A were shared with Session B")
	}

	paramsB["metadata"].(map[string]string)["mode"] = "session-b-mutated"
	paramsB["rules"].([]map[string]any)[0]["name"] = "session-b-mutated"
	if paramsA["metadata"].(map[string]string)["mode"] != "session-a-mutated" || paramsA["rules"].([]map[string]any)[0]["name"] != "session-a-mutated" {
		t.Fatal("mutating Session B changed Session A")
	}
}
