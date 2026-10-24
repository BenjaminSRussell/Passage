package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"pgregory.net/rapid"
)

func TestLifecycleMachine(t *testing.T) {
	legal := map[[2]string]bool{
		{domain.StateDraft, domain.StateActive}:       true,
		{domain.StateActive, domain.StatePhaseOut}:    true,
		{domain.StatePhaseOut, domain.StateActive}:    true,
		{domain.StatePhaseOut, domain.StateObsolete}:  true,
		{domain.StateDraft, domain.StateDraft}:        true,
		{domain.StateActive, domain.StateActive}:      true,
		{domain.StatePhaseOut, domain.StatePhaseOut}:  true,
		{domain.StateObsolete, domain.StateObsolete}:  true,
	}
	states := []string{domain.StateDraft, domain.StateActive, domain.StatePhaseOut, domain.StateObsolete}
	for _, from := range states {
		for _, to := range states {
			got := domain.CanTransition(from, to)
			if got != legal[[2]string{from, to}] {
				t.Fatalf("%s → %s = %v", from, to, got)
			}
		}
	}
}

func TestLifecyclePropertyRejectsIllegal(t *testing.T) {
	states := []string{domain.StateDraft, domain.StateActive, domain.StatePhaseOut, domain.StateObsolete}
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SampledFrom(states).Draw(t, "from")
		to := rapid.SampledFrom(states).Draw(t, "to")
		err := domain.AssertLifecycleTransition(from, to, "")
		if domain.CanTransition(from, to) && err != nil {
			t.Fatalf("legal %s→%s rejected: %v", from, to, err)
		}
		if !domain.CanTransition(from, to) && err == nil {
			t.Fatalf("illegal %s→%s accepted", from, to)
		}
	})
}

func TestProcessPolicyPhaseOutAndBlockFlag(t *testing.T) {
	states := domain.SeededLifecycleStates()
	rp := domain.ReleasedProduct{LifecycleState: "PHASE_OUT"}
	po := domain.EvaluateProcessPolicy("purchase_order", rp, domain.StateActive, states)
	if po.Code != domain.CodeLCBlocked {
		t.Fatalf("purchase_order: %+v", po)
	}
	so := domain.EvaluateProcessPolicy("sales_order", rp, domain.StateActive, states)
	if so.Code != domain.CodeLCWarn {
		t.Fatalf("sales_order: %+v", so)
	}
	rp.BlockedSales = true
	blocked := domain.EvaluateProcessPolicy("sales_order", rp, domain.StateActive, states)
	if blocked.Code != domain.CodeLCBlocked {
		t.Fatalf("blocked_sales: %+v", blocked)
	}
}

func TestReleasedCannotBeMoreAliveThanProduct(t *testing.T) {
	if err := domain.AssertLifecycleTransition(domain.StatePhaseOut, domain.StateActive, domain.StateObsolete); err == nil {
		t.Fatal("released active while product obsolete must fail")
	}
}
