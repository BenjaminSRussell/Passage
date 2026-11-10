package domain

import (
	"errors"
	"reflect"
	"testing"
)

func pair(lag int) []Region {
	return []Region{{Name: "eu-west", Role: Passive, LagSeconds: lag}, {Name: "us-east", Role: Primary}}
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestPlanFailoverFencesOldPrimaryAndPromotesTarget(t *testing.T) {
	plan, err := PlanFailover(pair(12), FailoverRequest{To: "eu-west", MaxLagSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if plan.From != "us-east" || plan.To != "eu-west" || plan.AcceptDataLoss {
		t.Fatalf("plan = %+v", plan)
	}
	var actions []string
	for _, s := range plan.Steps {
		actions = append(actions, s.Action)
	}
	want := []string{"fence-old-primary", "promote-cnpg", "switch-mirrormaker", "activate-temporal", "flip-dns", "verify-ledger-hash-chain"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("steps = %v", actions)
	}
	if plan.Steps[0].Region != "us-east" || plan.Steps[1].Region != "eu-west" {
		t.Fatalf("regions = %+v", plan.Steps)
	}
	after := plan.Apply(pair(12))
	if after[0].Role != Primary || after[1].Role != Fenced || ValidateTopology(after) != nil {
		t.Fatalf("after = %+v", after)
	}
}

func TestPlanFailoverLagNeedsAcceptanceAndASecondApprover(t *testing.T) {
	cases := []struct {
		name string
		req  FailoverRequest
		want string
	}{
		{"lag over the limit", FailoverRequest{To: "eu-west", MaxLagSeconds: 300}, CodeLagExceeded},
		{"accepted without approver", FailoverRequest{To: "eu-west", MaxLagSeconds: 300, AcceptDataLoss: true, Caller: "a"}, CodeApprovalRequired},
		{"approver is the caller", FailoverRequest{To: "eu-west", MaxLagSeconds: 300, AcceptDataLoss: true, Caller: "a", Approver: "a"}, CodeApprovalRequired},
		{"second approver", FailoverRequest{To: "eu-west", MaxLagSeconds: 300, AcceptDataLoss: true, Caller: "a", Approver: "b"}, ""},
	}
	for _, c := range cases {
		plan, err := PlanFailover(pair(301), c.req)
		if code(err) != c.want {
			t.Errorf("%s: err = %v, want code %q", c.name, err, c.want)
		}
		if c.want == "" && !plan.AcceptDataLoss {
			t.Errorf("%s: data loss not recorded", c.name)
		}
	}
	if _, err := PlanFailover(pair(300), FailoverRequest{To: "eu-west", MaxLagSeconds: 300}); err != nil {
		t.Errorf("lag at the limit must pass: %v", err)
	}
}

func TestPlanFailoverRejectsWrongTargets(t *testing.T) {
	fenced := []Region{{Name: "eu-west", Role: Fenced}, {Name: "us-east", Role: Primary}}
	cases := []struct {
		name    string
		regions []Region
		to      string
		want    string
	}{
		{"primary to itself", pair(0), "us-east", CodeTransitionInvalid},
		{"fenced is not promotable", fenced, "eu-west", CodeTransitionInvalid},
		{"unknown", pair(0), "ap-south", CodeRegionUnknown},
		{"no primary", []Region{{Name: "a", Role: Passive}, {Name: "b", Role: Passive}}, "a", CodeNoPrimary},
		{"two primaries", []Region{{Name: "a", Role: Primary}, {Name: "b", Role: Primary}}, "a", CodeTransitionInvalid},
	}
	for _, c := range cases {
		if _, err := PlanFailover(c.regions, FailoverRequest{To: c.to, MaxLagSeconds: 300}); code(err) != c.want {
			t.Errorf("%s: err = %v, want %s", c.name, err, c.want)
		}
	}
}

func TestRejoinOnlyAfterVerifiedResync(t *testing.T) {
	f := Region{Name: "us-east", Role: Fenced}
	if _, err := Rejoin(f, false); code(err) != CodeResyncUnverified {
		t.Fatalf("err = %v", err)
	}
	r, err := Rejoin(f, true)
	if err != nil || r.Role != Passive {
		t.Fatalf("r = %+v, err = %v", r, err)
	}
	if _, err := Rejoin(Region{Name: "x", Role: Primary}, true); code(err) != CodeTransitionInvalid {
		t.Fatalf("primary rejoin: %v", err)
	}
	if _, err := Rejoin(Region{Name: "x", Role: Passive}, true); code(err) != CodeTransitionInvalid {
		t.Fatalf("passive rejoin: %v", err)
	}
}

func TestParseRegions(t *testing.T) {
	got, err := ParseRegions("us-east=PRIMARY, eu-west=PASSIVE")
	if err != nil || len(got) != 2 || got[1].Name != "eu-west" || got[1].Role != Passive {
		t.Fatalf("got %+v, err %v", got, err)
	}
	for _, bad := range []string{"us-east", "a=PRIMARY,b=PRIMARY", "a=PASSIVE,a=PASSIVE", "a=ACTIVE", "=PRIMARY"} {
		if _, err := ParseRegions(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCapacityMatchesTheReferenceModel(t *testing.T) {
	// Figures from docs/ops/tools/capacity_estimate.py with the same inputs and rates.
	got, err := EstimateCapacity(DefaultCapacityInputs(), Rates{NodeUSDMonth: "610.00", PgGiBUSDMonth: "0.115", BrokerUSDMonth: "420.00"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pods.API != 20 || got.Pods.Ledger != 14 || got.Pods.Order != 3 || got.Pods.Primary != 37 || got.Pods.Passive != 19 {
		t.Errorf("pods = %+v", got.Pods)
	}
	if got.Nodes.Primary != 7 || got.Nodes.Passive != 4 || got.Postgres.StorageGiB != 4500 || got.Kafka.Brokers != 6 || got.Kafka.PartitionsMinPerTopic != 16 {
		t.Errorf("nodes/postgres/kafka = %+v %+v %+v", got.Nodes, got.Postgres, got.Kafka)
	}
	if got.Derived.InteractiveRequestsPerS != 1000 || got.Derived.JournalLinesPerSAverage != 277.78 || got.Derived.OrderLinesPerS != 27.78 || got.Derived.ScansPerS != 400 {
		t.Errorf("derived = %+v", got.Derived)
	}
	if got.CostUSDMonth != "16637.60" || !reflect.DeepEqual(got.CostPriced, []string{"kafka", "nodes", "postgres"}) {
		t.Errorf("cost = %s %v", got.CostUSDMonth, got.CostPriced)
	}
	small := DefaultCapacityInputs()
	small.Users, small.JournalLinesPerHour, small.OrderLinesPerHour, small.Scanners = 500, 50_000, 5_000, 20
	got, err = EstimateCapacity(small, Rates{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pods.API != 2 || got.Pods.Primary != 19 || got.Pods.Passive != 10 || got.Nodes.Primary != 4 || got.Nodes.Passive != 2 || got.CostUSDMonth != "" {
		t.Errorf("small = %+v", got)
	}
}

func TestCapacityRejectsBadInputsAndRates(t *testing.T) {
	in := DefaultCapacityInputs()
	in.Users = 0
	if _, err := EstimateCapacity(in, Rates{}); err == nil {
		t.Error("zero users accepted")
	}
	for _, r := range []Rates{{NodeUSDMonth: "-1"}, {PgGiBUSDMonth: "abc"}, {BrokerUSDMonth: "1e999999999"}} {
		if _, err := EstimateCapacity(DefaultCapacityInputs(), r); err == nil {
			t.Errorf("rates %+v accepted", r)
		}
	}
}

func TestRoundCentsHalfUp(t *testing.T) {
	for in, want := range map[string]string{"0.005": "0.01", "0.004": "0.00", "1234.565": "1234.57", "7": "7.00"} {
		r, _ := parseRate("x", in)
		if got := roundCents(r); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
