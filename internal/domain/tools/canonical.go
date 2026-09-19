package tools

import "time"

const (
	ToolCalendarCheckAvailability = "calendar.check_availability"
	ToolCalendarCreateEvent       = "calendar.create_event"
	ToolWhatsAppSendMessage       = "whatsapp.send_message"
	ToolMemorySearch              = "memory.search"
	ToolMemoryStore               = "memory.store"
	ToolLeadUpdate                = "lead.update"
	ToolConversationAddNote       = "conversation.add_note"
	ToolCallbackSchedule          = "callback.schedule"
)

func InitialToolNames() []string {
	return []string{
		ToolCalendarCheckAvailability,
		ToolCalendarCreateEvent,
		ToolWhatsAppSendMessage,
		ToolMemorySearch,
		ToolMemoryStore,
		ToolLeadUpdate,
		ToolConversationAddNote,
		ToolCallbackSchedule,
	}
}

func CanonicalToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolCalendarCheckAvailability,
			Description: "Check calendar availability for a given lead or time slot",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"start_time", "end_time"},
				Properties: map[string]string{"start_time": "string", "end_time": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"available": "boolean", "slots": "array"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup"},
			Timeout:             5 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 1 * time.Second},
			IdempotencyStrategy: "idempotency.strict",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "google_calendar", AdapterType: "calendar_adapter"},
		},
		{
			Name:        ToolCalendarCreateEvent,
			Description: "Create a calendar event/demo for a lead",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"lead_id", "start_time", "summary"},
				Properties: map[string]string{"lead_id": "string", "start_time": "string", "summary": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"event_id": "string", "status": "string"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup"},
			Timeout:             10 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 2 * time.Second},
			IdempotencyStrategy: "idempotency.payload_hash",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "google_calendar", AdapterType: "calendar_adapter"},
		},
		{
			Name:        ToolWhatsAppSendMessage,
			Description: "Send WhatsApp message fallback to a lead",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"phone_number", "message"},
				Properties: map[string]string{"phone_number": "string", "message": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"message_id": "string", "delivered": "boolean"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup", "post_call"},
			Timeout:             8 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 2, InitialBackoff: 500 * time.Millisecond, MaxBackoff: 2 * time.Second},
			IdempotencyStrategy: "idempotency.payload_hash",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "whatsapp_cloud_api", AdapterType: "messaging_adapter"},
		},
		{
			Name:        ToolMemorySearch,
			Description: "Search contextual memory for lead history or past notes",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"query"},
				Properties: map[string]string{"query": "string", "lead_id": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"memories": "array"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup", "post_call"},
			Timeout:             3 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 2, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
			IdempotencyStrategy: "idempotency.none",
			AuditPolicy:         AuditPolicy{LogPayload: false, MaskPII: true, AuditLevel: "debug"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "shared_agent_memory", AdapterType: "memory_adapter"},
		},
		{
			Name:        ToolMemoryStore,
			Description: "Store durable insight into shared memory",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"content", "memory_type"},
				Properties: map[string]string{"content": "string", "memory_type": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"memory_id": "string", "stored": "boolean"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup", "post_call"},
			Timeout:             5 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 1 * time.Second},
			IdempotencyStrategy: "idempotency.strict",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "shared_agent_memory", AdapterType: "memory_adapter"},
		},
		{
			Name:        ToolLeadUpdate,
			Description: "Update lead status and profile attributes",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"lead_id"},
				Properties: map[string]string{"lead_id": "string", "status": "string", "qualification": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"updated": "boolean"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup", "post_call"},
			Timeout:             5 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 1 * time.Second},
			IdempotencyStrategy: "idempotency.strict",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "crm_outbox", AdapterType: "database_adapter"},
		},
		{
			Name:        ToolConversationAddNote,
			Description: "Add structured summary note to call session transcript",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"call_id", "note"},
				Properties: map[string]string{"call_id": "string", "note": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"note_id": "string", "added": "boolean"},
			},
			AllowedContexts:     []string{"outbound_call", "post_call"},
			Timeout:             3 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 500 * time.Millisecond},
			IdempotencyStrategy: "idempotency.strict",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "transcript_outbox", AdapterType: "database_adapter"},
		},
		{
			Name:        ToolCallbackSchedule,
			Description: "Schedule retry callback for lead within business hours",
			InputSchema: SchemaDefinition{
				Type:       "object",
				Required:   []string{"lead_id", "callback_time"},
				Properties: map[string]string{"lead_id": "string", "callback_time": "string"},
			},
			OutputSchema: SchemaDefinition{
				Type:       "object",
				Properties: map[string]string{"scheduled": "boolean", "retry_at": "string"},
			},
			AllowedContexts:     []string{"outbound_call", "whatsapp_followup", "post_call"},
			Timeout:             5 * time.Second,
			RetryPolicy:         RetryPolicyMetadata{MaxRetries: 3, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 1 * time.Second},
			IdempotencyStrategy: "idempotency.strict",
			AuditPolicy:         AuditPolicy{LogPayload: true, MaskPII: true, AuditLevel: "info"},
			ProviderAdapter:     ProviderAdapterIdentifier{ProviderName: "scheduling_engine", AdapterType: "queue_adapter"},
		},
	}
}
