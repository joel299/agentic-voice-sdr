package tools

import (
	"strings"
	"testing"
)

func TestInitialToolNames(t *testing.T) {
	names := InitialToolNames()
	if len(names) != 8 {
		t.Fatalf("expected exactly 8 initial tool names, got %d", len(names))
	}

	seen := make(map[string]bool)
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate initial tool name found: %s", n)
		}
		seen[n] = true

		// Check format namespace.action
		parts := strings.Split(n, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Fatalf("initial tool name %s must be in namespace.action format", n)
		}
	}
}
