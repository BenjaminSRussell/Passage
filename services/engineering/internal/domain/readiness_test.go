package domain

import "testing"

func TestEvaluateReadiness_PendingUntilManualSignedAndBOMActive(t *testing.T) {
	policy := ReadinessPolicy{
		Code: "RDY", TargetState: ReleasedActive,
		Checks: []ReadinessCheck{
			{Key: "item_model_group", Kind: CheckAuto, Mandatory: true},
			{Key: "active_bom", Kind: CheckAuto, Mandatory: true},
			{Key: "active_route", Kind: CheckAuto, Mandatory: true},
			{Key: "quality_signoff", Kind: CheckManual, Mandatory: true, Assignee: "qa"},
		},
	}
	facts := ReadinessFacts{ItemModelGroupSet: true, ActiveBOM: false, ActiveRoute: true}
	results := EvaluateReadiness(policy, facts, nil)
	state, pending := ReadinessState(policy, results)
	if state != ReadinessPending || pending != 2 {
		t.Fatalf("state=%s pending=%d; want pending with BOM and manual still open", state, pending)
	}
	if got := ProcessPolicy(state, "sales_order"); got != CodeLifecycleBlocked {
		t.Fatalf("sales order while pending = %s; want %s", got, CodeLifecycleBlocked)
	}

	results = EvaluateReadiness(policy, ReadinessFacts{ItemModelGroupSet: true, ActiveBOM: true, ActiveRoute: true}, results)
	signed, err := SignManualCheck(results, "quality_signoff", "qa", "rita")
	if err != nil {
		t.Fatal(err)
	}
	state, pending = ReadinessState(policy, signed)
	if state != ReleasedActive || pending != 0 {
		t.Fatalf("after sign and active BOM: state=%s pending=%d", state, pending)
	}
}

func TestSignManualCheck_CreatorCannotSign(t *testing.T) {
	results := []ReadinessCheckResult{{Key: "quality_signoff", Kind: CheckManual, Status: CheckPending}}
	if _, err := SignManualCheck(results, "quality_signoff", "rita", "rita"); !HasCode(err, CodeReadinessSelfSign) {
		t.Fatalf("creator sign = %v; want %s", err, CodeReadinessSelfSign)
	}
}

func TestEvaluateReadiness_MissingWaveHooksAreNotApplicable(t *testing.T) {
	policy := ReadinessPolicy{Checks: []ReadinessCheck{
		{Key: "tax_group", Kind: CheckAuto, Mandatory: true},
		{Key: "coverage_group", Kind: CheckAuto, Mandatory: true},
	}}
	results := EvaluateReadiness(policy, ReadinessFacts{}, nil)
	for _, r := range results {
		if r.Status != CheckNotApplicable {
			t.Fatalf("%s = %s; want not_applicable until the owning wave is live", r.Key, r.Status)
		}
	}
	state, pending := ReadinessState(policy, results)
	if state != ReleasedActive || pending != 0 {
		t.Fatalf("only NA checks: state=%s pending=%d", state, pending)
	}
}
