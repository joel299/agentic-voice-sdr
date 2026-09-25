package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidDecisionScript          = errors.New("invalid decision script")
	ErrDecisionScriptExhausted        = errors.New("decision script exhausted")
	ErrInvalidDecisionProviderContext = errors.New("decision provider context must not be nil")
)

// DecisionProvider supplies a canonical domain decision for a canonical input.
//
// The caller constructs and validates DecisionInput through the domain boundary
// before calling Decide. Providers decide only the next action and reason; they
// do not construct conversation state, generate spoken copy, or execute tools.
// The context must be non-nil; implementations must respect caller cancellation.
type DecisionProvider interface {
	Decide(ctx context.Context, input DecisionInput) (Decision, error)
}

// ScriptedDecisionProvider is an explicit deterministic FIFO provider for tests
// and development. It does not infer or add validation semantics to the
// caller-provided DecisionInput.
type ScriptedDecisionProvider struct {
	mu        sync.Mutex
	decisions []Decision
	next      int
}

func NewScriptedDecisionProvider(script []Decision) (*ScriptedDecisionProvider, error) {
	decisions := make([]Decision, len(script))
	copy(decisions, script)
	for index, decision := range decisions {
		if err := decision.Validate(); err != nil {
			return nil, fmt.Errorf("%w at index %d: %v", ErrInvalidDecisionScript, index, err)
		}
	}
	return &ScriptedDecisionProvider{decisions: decisions}, nil
}

func (p *ScriptedDecisionProvider) Decide(ctx context.Context, input DecisionInput) (Decision, error) {
	if ctx == nil {
		return Decision{}, ErrInvalidDecisionProviderContext
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if p.next >= len(p.decisions) {
		return Decision{}, ErrDecisionScriptExhausted
	}
	decision := p.decisions[p.next]
	p.next++
	return decision, nil
}
