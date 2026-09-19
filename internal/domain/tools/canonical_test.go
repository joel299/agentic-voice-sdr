package tools

import "testing"

func TestInitialToolNamesUnique(t *testing.T) {
	names := InitialToolNames()
	if len(names) != 8 {
		t.Fatalf("expected 8 initial tool names, got %d", len(names))
	}

	seen := make(map[string]bool)
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate initial tool name: %s", n)
		}
		seen[n] = true
	}
}

func TestCanonicalToolDefinitionsValidAndRegistrable(t *testing.T) {
	defs := CanonicalToolDefinitions()
	if len(defs) != 8 {
		t.Fatalf("expected 8 canonical tool definitions, got %d", len(defs))
	}

	r := NewInMemoryRegistry()
	for _, def := range defs {
		if err := def.Validate(); err != nil {
			t.Fatalf("canonical tool definition %s invalid: %v", def.Name, err)
		}
		if err := r.Register(def); err != nil {
			t.Fatalf("failed to register canonical tool %s: %v", def.Name, err)
		}
	}

	if len(r.List()) != 8 {
		t.Fatalf("expected 8 registered tools, got %d", len(r.List()))
	}
}
