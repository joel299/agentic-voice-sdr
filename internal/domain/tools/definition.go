package tools

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

type RetryPolicyMetadata struct {
	MaxRetries     int           `json:"max_retries"`
	InitialBackoff time.Duration `json:"initial_backoff"`
	MaxBackoff     time.Duration `json:"max_backoff"`
}

func (rpm RetryPolicyMetadata) Validate() error {
	if rpm.MaxRetries < 0 {
		return fmt.Errorf("%w: max_retries cannot be negative", ErrInvalidDefinition)
	}
	if rpm.MaxRetries > 0 {
		if rpm.InitialBackoff <= 0 {
			return fmt.Errorf("%w: initial_backoff must be greater than zero when max_retries > 0", ErrInvalidDefinition)
		}
		if rpm.MaxBackoff < rpm.InitialBackoff {
			return fmt.Errorf("%w: max_backoff cannot be less than initial_backoff", ErrInvalidDefinition)
		}
	}
	return nil
}

type AuditPolicy struct {
	LogPayload bool   `json:"log_payload"`
	MaskPII    bool   `json:"mask_pii"`
	AuditLevel string `json:"audit_level"`
}

func (ap AuditPolicy) Validate() error {
	if strings.TrimSpace(ap.AuditLevel) == "" {
		return fmt.Errorf("%w: audit_level cannot be empty", ErrInvalidDefinition)
	}
	return nil
}

type ProviderAdapterIdentifier struct {
	ProviderName string `json:"provider_name"`
	AdapterType  string `json:"adapter_type"`
}

func (pai ProviderAdapterIdentifier) Validate() error {
	if strings.TrimSpace(pai.ProviderName) == "" {
		return fmt.Errorf("%w: provider_name cannot be empty", ErrInvalidDefinition)
	}
	if strings.TrimSpace(pai.AdapterType) == "" {
		return fmt.Errorf("%w: adapter_type cannot be empty", ErrInvalidDefinition)
	}
	return nil
}

type ToolDefinition struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	InputSchema         SchemaDefinition          `json:"input_schema"`
	OutputSchema        SchemaDefinition          `json:"output_schema"`
	AllowedContexts     []string                  `json:"allowed_contexts"`
	Timeout             time.Duration             `json:"timeout"`
	RetryPolicy         *RetryPolicyMetadata      `json:"retry_policy"`
	IdempotencyStrategy string                    `json:"idempotency_strategy"`
	AuditPolicy         AuditPolicy               `json:"audit_policy"`
	ProviderAdapter     ProviderAdapterIdentifier `json:"provider_adapter"`
}

func validateToolName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name cannot be empty", ErrInvalidDefinition)
	}
	for _, r := range name {
		if unicode.IsSpace(r) {
			return fmt.Errorf("%w: name cannot contain whitespace", ErrInvalidDefinition)
		}
	}
	parts := strings.Split(name, ".")
	if len(parts) != 2 {
		return fmt.Errorf("%w: name must be in namespace.action format (got %q)", ErrInvalidDefinition, name)
	}
	if parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("%w: namespace and action in name cannot be empty (got %q)", ErrInvalidDefinition, name)
	}
	return nil
}

func (td ToolDefinition) Validate() error {
	if err := validateToolName(td.Name); err != nil {
		return err
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
	if td.RetryPolicy == nil {
		return fmt.Errorf("%w: retry_policy cannot be nil", ErrInvalidDefinition)
	}
	if err := td.RetryPolicy.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(td.IdempotencyStrategy) == "" {
		return fmt.Errorf("%w: idempotency strategy cannot be empty", ErrInvalidDefinition)
	}
	if err := td.AuditPolicy.Validate(); err != nil {
		return err
	}
	if err := td.ProviderAdapter.Validate(); err != nil {
		return err
	}
	return nil
}

func (td ToolDefinition) Clone() ToolDefinition {
	var ctxCopy []string
	if td.AllowedContexts != nil {
		ctxCopy = make([]string, len(td.AllowedContexts))
		copy(ctxCopy, td.AllowedContexts)
	}
	var retryCopy *RetryPolicyMetadata
	if td.RetryPolicy != nil {
		val := *td.RetryPolicy
		retryCopy = &val
	}
	return ToolDefinition{
		Name:                td.Name,
		Description:         td.Description,
		InputSchema:         td.InputSchema.Clone(),
		OutputSchema:        td.OutputSchema.Clone(),
		AllowedContexts:     ctxCopy,
		Timeout:             td.Timeout,
		RetryPolicy:         retryCopy,
		IdempotencyStrategy: td.IdempotencyStrategy,
		AuditPolicy:         td.AuditPolicy,
		ProviderAdapter:     td.ProviderAdapter,
	}
}
