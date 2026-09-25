package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidDecisionScript   = errors.New("invalid decision script")
	ErrDecisionScriptExhausted = errors.New("decision script exhausted")
)

type DecisionProvider interface {
	Decide(ctx context.Context, input DecisionInput) (Decision, error)
}

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
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := input.validate(); err != nil {
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
