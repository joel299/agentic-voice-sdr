package conversation

import "testing"

func TestDecisionAcceptsConfirmedInterestForScheduling(t *testing.T) {
	decision, err := NewDecision(ActionProposeScheduling, ReasonInterestConfirmed)
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	if decision.NextAction != ActionProposeScheduling || decision.Reason != ReasonInterestConfirmed {
		t.Fatalf("decision = %+v, want scheduling with confirmed interest", decision)
	}
}
