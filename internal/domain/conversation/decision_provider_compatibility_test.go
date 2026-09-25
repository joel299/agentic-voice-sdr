package conversation_test

import (
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/domain/conversation"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/openrouterjev"
)

var _ conversation.DecisionProvider = (*openrouterjev.Client)(nil)

func TestOpenRouterJEVAdapterSatisfiesDecisionProvider(t *testing.T) {
	t.Log("OpenRouter JEV adapter satisfies the provider-neutral DecisionProvider boundary")
}
