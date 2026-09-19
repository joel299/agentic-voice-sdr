package tools

import (
	"errors"
	"testing"
	"time"
)

func sampleTestDefinition(name string) ToolDefinition {
	return ToolDefinition{
		Name:        name,
		Description: "Test tool description for " + name,
		InputSchema: SchemaDefinition{
			Type:       "object",
			Required:   []string{"req_param"},
			Properties: map[string]string{"req_param": "string"},
		},
		OutputSchema: SchemaDefinition{
			Type:       "object",
			Properties: map[string]string{"result": "string"},
		},
		AllowedContexts:     []string{"outbound_call", "test_context"},
		Timeout:             5 * time.Second,
		RetryPolicy:         RetryPolicyMetadata{MaxRetries: 2, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
		IdempotencyStrategy: "test_strategy",
		AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
		ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "test_provider", AdapterType: "test_adapter"},
	}
}

func TestToolDefinitionValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ToolDefinition)
		wantErr error
	}{
		{
			name:    "valid definition",
			mutate:  func(td *ToolDefinition) {},
			wantErr: nil,
		},
		{
			name: "empty name",
			mutate: func(td *ToolDefinition) {
				td.Name = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name with space",
			mutate: func(td *ToolDefinition) {
				td.Name = "calendar check"
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name with tab",
			mutate: func(td *ToolDefinition) {
				td.Name = "calendar\tcheck"
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name without namespace prefix",
			mutate: func(td *ToolDefinition) {
				td.Name = "tool"
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name with leading dot",
			mutate: func(td *ToolDefinition) {
				td.Name = ".tool"
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name with trailing dot",
			mutate: func(td *ToolDefinition) {
				td.Name = "tool."
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "name with double dot",
			mutate: func(td *ToolDefinition) {
				td.Name = "tool..name"
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty description",
			mutate: func(td *ToolDefinition) {
				td.Description = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty input schema type",
			mutate: func(td *ToolDefinition) {
				td.InputSchema.Type = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty element in input schema required",
			mutate: func(td *ToolDefinition) {
				td.InputSchema.Required = []string{""}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty property name in input schema",
			mutate: func(td *ToolDefinition) {
				td.InputSchema.Properties = map[string]string{"": "string"}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty property type in input schema",
			mutate: func(td *ToolDefinition) {
				td.InputSchema.Properties = map[string]string{"param": ""}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "required field missing from properties map",
			mutate: func(td *ToolDefinition) {
				td.InputSchema.Required = []string{"non_existent_field"}
				td.InputSchema.Properties = map[string]string{"existing_field": "string"}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty output schema type",
			mutate: func(td *ToolDefinition) {
				td.OutputSchema.Type = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty allowed contexts",
			mutate: func(td *ToolDefinition) {
				td.AllowedContexts = []string{}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "allowed context with whitespace string",
			mutate: func(td *ToolDefinition) {
				td.AllowedContexts = []string{"   "}
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "zero timeout",
			mutate: func(td *ToolDefinition) {
				td.Timeout = 0
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "negative timeout",
			mutate: func(td *ToolDefinition) {
				td.Timeout = -1 * time.Second
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "negative retry max_retries",
			mutate: func(td *ToolDefinition) {
				td.RetryPolicy.MaxRetries = -1
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "zero initial backoff when max_retries > 0",
			mutate: func(td *ToolDefinition) {
				td.RetryPolicy.MaxRetries = 3
				td.RetryPolicy.InitialBackoff = 0
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "max_backoff less than initial_backoff",
			mutate: func(td *ToolDefinition) {
				td.RetryPolicy.MaxRetries = 3
				td.RetryPolicy.InitialBackoff = 500 * time.Millisecond
				td.RetryPolicy.MaxBackoff = 100 * time.Millisecond
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty idempotency strategy",
			mutate: func(td *ToolDefinition) {
				td.IdempotencyStrategy = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty audit level",
			mutate: func(td *ToolDefinition) {
				td.AuditPolicy.AuditLevel = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty provider name",
			mutate: func(td *ToolDefinition) {
				td.ProviderAdapter.ProviderName = ""
			},
			wantErr: ErrInvalidDefinition,
		},
		{
			name: "empty provider adapter type",
			mutate: func(td *ToolDefinition) {
				td.ProviderAdapter.AdapterType = ""
			},
			wantErr: ErrInvalidDefinition,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			td := sampleTestDefinition("calendar.check_availability")
			tt.mutate(&td)
			err := td.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error wrapping %v, got: %v", tt.wantErr, err)
				}
			}
		})
	}
}
