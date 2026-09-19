package tools

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

// InitialToolNames returns the canonical list of SDD-mandated tool names.
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
