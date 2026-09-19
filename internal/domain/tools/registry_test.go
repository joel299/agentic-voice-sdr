package tools

import (
	"errors"
	"testing"
)

func TestInMemoryRegistryRegisterAndGet(t *testing.T) {
	r := NewInMemoryRegistry()
	def := sampleTestDefinition("calendar.check_availability")

	if err := r.Register(def); err != nil {
		t.Fatalf("failed to register valid tool: %v", err)
	}

	got, err := r.Get("calendar.check_availability")
	if err != nil {
		t.Fatalf("expected tool to be found, got: %v", err)
	}
	if got.Name != def.Name {
		t.Fatalf("expected name %s, got %s", def.Name, got.Name)
	}
}

func TestInMemoryRegistryRejectsDuplicates(t *testing.T) {
	r := NewInMemoryRegistry()
	def := sampleTestDefinition("whatsapp.send_message")

	if err := r.Register(def); err != nil {
		t.Fatalf("first register failed: %v", err)
	}

	err := r.Register(def)
	if err == nil {
		t.Fatal("expected duplicate error, got nil")
	}
	if !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("expected ErrDuplicateTool, got: %v", err)
	}
}

func TestInMemoryRegistryRejectsInvalidDefinition(t *testing.T) {
	r := NewInMemoryRegistry()
	def := sampleTestDefinition("invalid_name")

	err := r.Register(def)
	if err == nil {
		t.Fatal("expected invalid definition error, got nil")
	}
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("expected ErrInvalidDefinition, got: %v", err)
	}
}

func TestInMemoryRegistryGetUnknown(t *testing.T) {
	r := NewInMemoryRegistry()
	_, err := r.Get("unknown.tool")
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("expected ErrUnknownTool, got: %v", err)
	}
}

func TestInMemoryRegistryAllowlistAndContextValidation(t *testing.T) {
	r := NewInMemoryRegistry()
	def := sampleTestDefinition("lead.update")
	def.AllowedContexts = []string{"outbound_call"}

	if err := r.Register(def); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Registered tool allowed context
	if !r.IsAllowedInContext("lead.update", "outbound_call") {
		t.Fatal("expected lead.update to be allowed in outbound_call")
	}
	if err := r.ValidateAllowedInContext("lead.update", "outbound_call"); err != nil {
		t.Fatalf("expected no error for allowed context, got: %v", err)
	}

	// Registered tool denied context
	if r.IsAllowedInContext("lead.update", "unauthorized_context") {
		t.Fatal("expected lead.update to be denied in unauthorized_context")
	}
	err := r.ValidateAllowedInContext("lead.update", "unauthorized_context")
	if err == nil || !errors.Is(err, ErrToolNotAllowedInContext) {
		t.Fatalf("expected ErrToolNotAllowedInContext, got: %v", err)
	}

	// Allowlist-first check: Arbitrary unregistered tools MUST be rejected
	arbitraryTools := []string{"dangerous.execute", "admin.delete_everything", "unknown.tool"}
	for _, tool := range arbitraryTools {
		if r.IsAllowedInContext(tool, "outbound_call") {
			t.Fatalf("arbitrary unregistered tool %s must NOT be allowed", tool)
		}
		err := r.ValidateAllowedInContext(tool, "outbound_call")
		if err == nil || !errors.Is(err, ErrUnknownTool) {
			t.Fatalf("expected ErrUnknownTool for unregistered tool %s, got: %v", tool, err)
		}
	}
}

func TestInMemoryRegistryDeterministicList(t *testing.T) {
	r := NewInMemoryRegistry()

	// Insert in non-alphabetical order
	names := []string{"whatsapp.send_message", "calendar.check_availability", "memory.store", "lead.update"}
	for _, n := range names {
		if err := r.Register(sampleTestDefinition(n)); err != nil {
			t.Fatalf("failed to register %s: %v", n, err)
		}
	}

	list1 := r.List()
	list2 := r.List()

	if len(list1) != len(names) {
		t.Fatalf("expected list length %d, got %d", len(names), len(list1))
	}

	expectedOrder := []string{"calendar.check_availability", "lead.update", "memory.store", "whatsapp.send_message"}
	for i, expected := range expectedOrder {
		if list1[i].Name != expected {
			t.Fatalf("list1 index %d expected %s, got %s", i, expected, list1[i].Name)
		}
		if list2[i].Name != expected {
			t.Fatalf("list2 index %d expected %s, got %s", i, expected, list2[i].Name)
		}
	}
}

func TestInMemoryRegistryImmutabilityDefensiveCopy(t *testing.T) {
	r := NewInMemoryRegistry()

	inputDef := sampleTestDefinition("memory.search")
	inputDef.AllowedContexts = []string{"outbound_call"}
	inputDef.InputSchema.Required = []string{"req_param"}
	inputDef.InputSchema.Properties = map[string]string{"req_param": "string"}
	inputDef.OutputSchema.Required = []string{"result"}
	inputDef.OutputSchema.Properties = map[string]string{"result": "string"}

	if err := r.Register(inputDef); err != nil {
		t.Fatalf("failed to register tool: %v", err)
	}

	// 1. Mutate original input struct fields after Register
	inputDef.AllowedContexts[0] = "MUTATED"
	inputDef.InputSchema.Required[0] = "MUTATED"
	inputDef.InputSchema.Properties["req_param"] = "MUTATED"
	inputDef.OutputSchema.Required[0] = "MUTATED"
	inputDef.OutputSchema.Properties["result"] = "MUTATED"

	got, err := r.Get("memory.search")
	if err != nil {
		t.Fatalf("failed to get tool: %v", err)
	}

	if got.AllowedContexts[0] == "MUTATED" {
		t.Fatal("registry allowed contexts was mutated via input reference!")
	}
	if got.InputSchema.Required[0] == "MUTATED" {
		t.Fatal("registry input schema required slice was mutated via input reference!")
	}
	if got.InputSchema.Properties["req_param"] == "MUTATED" {
		t.Fatal("registry input schema properties map was mutated via input reference!")
	}
	if got.OutputSchema.Required[0] == "MUTATED" {
		t.Fatal("registry output schema required slice was mutated via input reference!")
	}
	if got.OutputSchema.Properties["result"] == "MUTATED" {
		t.Fatal("registry output schema properties map was mutated via input reference!")
	}

	// 2. Mutate returned struct fields after Get
	got.AllowedContexts[0] = "MUTATED_AFTER_GET"
	got.InputSchema.Properties["req_param"] = "MUTATED_AFTER_GET"
	got.OutputSchema.Properties["result"] = "MUTATED_AFTER_GET"

	got2, _ := r.Get("memory.search")
	if got2.AllowedContexts[0] == "MUTATED_AFTER_GET" {
		t.Fatal("registry allowed contexts was mutated via returned Get reference!")
	}
	if got2.InputSchema.Properties["req_param"] == "MUTATED_AFTER_GET" {
		t.Fatal("registry input schema properties was mutated via returned Get reference!")
	}
	if got2.OutputSchema.Properties["result"] == "MUTATED_AFTER_GET" {
		t.Fatal("registry output schema properties was mutated via returned Get reference!")
	}

	// 3. Mutate returned List elements
	list := r.List()
	list[0].AllowedContexts[0] = "MUTATED_LIST"
	list[0].InputSchema.Properties["req_param"] = "MUTATED_LIST"

	got3, _ := r.Get("memory.search")
	if got3.AllowedContexts[0] == "MUTATED_LIST" {
		t.Fatal("registry state was mutated via returned List reference!")
	}
	if got3.InputSchema.Properties["req_param"] == "MUTATED_LIST" {
		t.Fatal("registry state input schema properties was mutated via returned List reference!")
	}
}
