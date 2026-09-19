package tools

import (
	"fmt"
	"strings"
	"time"
)

type RetryPolicyMetadata struct {
	MaxRetries     int           `json:"max_retries"`
	InitialBackoff time.Duration `json:"initial_backoff"`
	MaxBackoff     time.Duration `json:"max_backoff"`
}

type AuditPolicy struct {
	LogPayload bool   `json:"log_payload"`
	MaskPII    bool   `json:"mask_pii"`
	AuditLevel string `json:"audit_level"`
}

type ProviderAdapterIdentifier struct {
	ProviderName string `json:"provider_name"`
	AdapterType  string `json:"adapter_type"`
}

type ToolDefinition struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	InputSchema         SchemaDefinition          `json:"input_schema"`
	OutputSchema        SchemaDefinition          `json:"output_schema"`
	AllowedContexts     []string                  `json:"allowed_contexts"`
	Timeout             time.Duration             `json:"timeout"`
	RetryPolicy         RetryPolicyMetadata       `json:"retry_policy"`
	IdempotencyStrategy string                    `json:"idempotency_strategy"`
	AuditPolicy         AuditPolicy               `json:"audit_policy"`
	ProviderAdapter     ProviderAdapterIdentifier `json:"provider_adapter"`
}

func (td ToolDefinition) Validate() error {
	if strings.TrimSpace(td.Name) == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidDefinition)
	}
	if strings.Contains(td.Name, " ") {
		return fmt.Errorf("%w: name cannot contain whitespace", ErrInvalidDefinition)
	}
	if strings.TrimSpace(td.Description) == "" {
		return fmt.Errorf("%w: description cannot be empty", ErrInvalidDefinition)
	}
	if err := td.InputSchema.Validate(); err != nil {
		return fmt.Errorf("%w: input schema invalid: %v", ErrInvalidDefinition, err)
	}
	if err := td.OutputSchema.Validate(); err != nil {
		return fmt.Errorf("%w: output schema invalid: %v", ErrInvalidDefinition, err)
	}
	if len(td.AllowedContexts) == 0 {
		return fmt.Errorf("%w: allowed contexts cannot be empty", ErrInvalidDefinition)
	}
	for i, ctx := range td.AllowedContexts {
		if strings.TrimSpace(ctx) == "" {
			return fmt.Errorf("%w: allowed context at index %d cannot be empty", ErrInvalidDefinition, i)
		}
	}
	if td.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be greater than zero", ErrInvalidDefinition)
	}
	return nil
}

func (td ToolDefinition) Clone() ToolDefinition {
	var ctxCopy []string
	if td.AllowedContexts != nil {
		ctxCopy = make([]string, len(td.AllowedContexts))
		copy(ctxCopy, td.AllowedContexts)
	}
	return ToolDefinition{
		Name:                td.Name,
		Description:         td.Description,
		InputSchema:         td.InputSchema.Clone(),
		OutputSchema:        td.OutputSchema.Clone(),
		AllowedContexts:     ctxCopy,
		Timeout:             td.Timeout,
		RetryPolicy:         td.RetryPolicy,
		IdempotencyStrategy: td.IdempotencyStrategy,
		AuditPolicy:         td.AuditPolicy,
		ProviderAdapter:     td.ProviderAdapter,
	}
}
