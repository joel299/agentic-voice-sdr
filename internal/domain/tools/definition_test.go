package tools

import (
	"errors"
	"testing"
	"time"
)

func validDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        "calendar.check_availability",
		Description: "Check calendar availability",
		InputSchema: SchemaDefinition{
			Type:       "object",
			Required:   []string{"start_time"},
			Properties: map[string]string{"start_time": "string"},
		},
		OutputSchema: SchemaDefinition{
			Type:       "object",
			Properties: map[string]string{"available": "boolean"},
		},
		AllowedContexts: []string{"outbound_call"},
		Timeout:         5 * time.Second,
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
			name: "name with whitespace",
			mutate: func(td *ToolDefinition) {
				td.Name = "calendar check"
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
			name: "allowed context with empty string",
			mutate: func(td *ToolDefinition) {
				td.AllowedContexts = []string{"  "}
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			td := validDefinition()
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
