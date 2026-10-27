package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var ncrNow = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) // a Monday

func validNCR() Nonconformance {
	return Nonconformance{Source: SourceVendor, ItemID: "item-1", Qty: decimal.NewFromInt(4), UOM: "EA", ProblemType: "DIMENSION", Severity: NCRMajor}
}

func code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestValidateNCR_Fields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Nonconformance)
		field  string
	}{
		{"unknown source", func(n *Nonconformance) { n.Source = "PIGEON" }, "source"},
		{"unknown severity", func(n *Nonconformance) { n.Severity = "LOW" }, "severity"},
		{"lower-case problem type", func(n *Nonconformance) { n.ProblemType = "dim" }, "problemType"},
		{"problem type too long", func(n *Nonconformance) { n.ProblemType = strings.Repeat("A", 33) }, "problemType"},
		{"zero qty", func(n *Nonconformance) { n.Qty = decimal.Zero }, "qty"},
		{"negative qty", func(n *Nonconformance) { n.Qty = decimal.NewFromInt(-1) }, "qty"},
		{"seven decimals", func(n *Nonconformance) { n.Qty = decimal.RequireFromString("0.0000001") }, "qty"},
		{"no unit", func(n *Nonconformance) { n.UOM = "" }, "uom"},
		{"long description", func(n *Nonconformance) { n.Description = strings.Repeat("x", MaxNCRDescription+1) }, "description"},
	}
	if fields := ValidateNCR(validNCR()); len(fields) != 0 {
		t.Fatalf("valid NCR: %v", fields)
	}
	for _, tc := range cases {
		n := validNCR()
		tc.mutate(&n)
		fields := ValidateNCR(n)
		if _, ok := fields[tc.field]; !ok || len(fields) != 1 {
			t.Errorf("%s: fields %v, want only %s", tc.name, fields, tc.field)
		}
	}
}

func TestNewNCR_StartsNewAtVersion1(t *testing.T) {
	n, err := NewNCR(validNCR(), "qe-1", ncrNow)
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != NCRNew || n.Version != 1 || n.CreatedBy != "qe-1" || !n.CreatedAt.Equal(ncrNow) || n.Dims == nil {
		t.Fatalf("unexpected NCR %+v", n)
	}
	bad := validNCR()
	bad.Severity = ""
	if _, err := NewNCR(bad, "qe-1", ncrNow); code(err) != CodeValidation {
		t.Fatalf("invalid NCR: %v", err)
	}
}

func TestCanNCRTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{NCRNew, NCRApproved, true},
		{NCRNew, NCRRefused, true},
		{NCRApproved, NCRInCorrection, true},
		{NCRInCorrection, NCRClosed, true},
		{NCRNew, NCRInCorrection, false},
		{NCRNew, NCRClosed, false},
		{NCRApproved, NCRRefused, false},
		{NCRRefused, NCRApproved, false},
		{NCRClosed, NCRInCorrection, false},
	}
	for _, tc := range cases {
		if got := CanNCRTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s: got %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestApproveRefuseStart_Guards(t *testing.T) {
	n, _ := NewNCR(validNCR(), "qe-1", ncrNow)
	if _, err := n.StartCorrection("qe-1", 0, ncrNow); code(err) != CodeNCRStatus {
		t.Fatalf("start on NEW: %v", err)
	}
	if _, err := n.Refuse(" ", ncrNow); code(err) != CodeValidation {
		t.Fatalf("refuse without reason: %v", err)
	}
	refused, err := n.Refuse("duplicate of NCR 12", ncrNow)
	if err != nil || refused.Status != NCRRefused || refused.StatusReason != "duplicate of NCR 12" {
		t.Fatalf("refuse: %+v %v", refused, err)
	}
	if _, err := refused.Approve(ncrNow); code(err) != CodeNCRStatus {
		t.Fatalf("approve a refused NCR: %v", err)
	}
	approved, err := n.Approve(ncrNow)
	if err != nil || approved.Status != NCRApproved {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := approved.CompleteStep(1, "team", "qe-1", ncrNow); code(err) != CodeCAPAStep {
		t.Fatalf("step before the correction starts: %v", err)
	}
	started, err := approved.StartCorrection("qe-1", 0, ncrNow)
	if err != nil || started.Status != NCRInCorrection || started.Correction == nil || started.Correction.EffectivenessDays != DefaultEffectivenessDays {
		t.Fatalf("start: %+v %v", started, err)
	}
	if _, err := started.StartCorrection("qe-1", 0, ncrNow); code(err) != CodeNCRStatus {
		t.Fatalf("second start: %v", err)
	}
}

// AC-33.4 (NCR leg): accept 90 / reject 10 raises an NCR for 10 linked to the order.
func TestNCRFromDecision(t *testing.T) {
	order := Order{ID: "qo-1", Trigger: TriggerPurchaseReceipt, ItemID: "item-1", UOM: "EA", Dims: map[string]string{"batch": "B1"}}
	lines := []DecisionLine{{Decision: DecisionAccept, Quantity: decimal.NewFromInt(90)}, {Decision: DecisionReject, Quantity: decimal.NewFromInt(10)}}
	split, err := SplitDecision(lines, decimal.NewFromInt(100))
	if err != nil {
		t.Fatal(err)
	}
	n, ok := NCRFromDecision(order, Decision{Lines: lines, Split: split, DecidedBy: "qa-1"}, ncrNow)
	if !ok || !n.Qty.Equal(decimal.NewFromInt(10)) || n.Source != SourceVendor || n.QualityOrderID != "qo-1" || n.UOM != "EA" ||
		n.Dims["batch"] != "B1" || n.Status != NCRNew || n.CreatedBy != "qa-1" || !strings.Contains(n.Description, "REJECT 10") {
		t.Fatalf("reject NCR: %+v %v", n, ok)
	}

	accept := []DecisionLine{{Decision: DecisionAccept, Quantity: decimal.NewFromInt(95)}, {Decision: DecisionAcceptWithDeviation, Quantity: decimal.NewFromInt(5), Reason: "cosmetic"}}
	split, _ = SplitDecision(accept, decimal.NewFromInt(100))
	if _, ok := NCRFromDecision(order, Decision{Lines: accept, Split: split}, ncrNow); ok {
		t.Fatal("an accepting decision raises no NCR")
	}

	sources := map[string]string{TriggerPurchaseReceipt: SourceVendor, TriggerSourceInspect: SourceVendor, TriggerProductionRAF: SourceProduction,
		TriggerSalesPick: SourceInternal, TriggerTransferReceipt: SourceInternal, TriggerManual: SourceInternal}
	scrap := []DecisionLine{{Decision: DecisionScrap, Quantity: decimal.NewFromInt(3)}, {Decision: DecisionRework, Quantity: decimal.NewFromInt(2)}}
	split, _ = SplitDecision(scrap, decimal.NewFromInt(5))
	for trigger, want := range sources {
		o := order
		o.Trigger, o.SourceRef = trigger, SourceRef{Type: "production_order", ID: "po-9"}
		n, ok := NCRFromDecision(o, Decision{Lines: scrap, Split: split}, ncrNow)
		if !ok || n.Source != want || !n.Qty.Equal(decimal.NewFromInt(5)) {
			t.Errorf("%s: source %s qty %s, want %s 5", trigger, n.Source, n.Qty, want)
		}
		if (n.ProductionOrderID == "po-9") != (trigger == TriggerProductionRAF) {
			t.Errorf("%s: production order link %q", trigger, n.ProductionOrderID)
		}
	}
}

// Property: along any sequence of requested transitions an NCR only moves along
// allowed edges, never leaves REFUSED or CLOSED, and reaches CLOSED only through
// IN_CORRECTION.
func TestNCRStatusWalk_Property(t *testing.T) {
	all := []string{NCRNew, NCRApproved, NCRRefused, NCRInCorrection, NCRClosed}
	rapid.Check(t, func(t *rapid.T) {
		cur, seen := NCRNew, map[string]bool{NCRNew: true}
		for range rapid.IntRange(0, 12).Draw(t, "steps") {
			next := rapid.SampledFrom(all).Draw(t, "next")
			if !CanNCRTransition(cur, next) {
				continue
			}
			if cur == NCRRefused || cur == NCRClosed {
				t.Fatalf("final %s moved to %s", cur, next)
			}
			cur = next
			seen[cur] = true
		}
		if cur == NCRClosed && !seen[NCRInCorrection] {
			t.Fatal("CLOSED reached without IN_CORRECTION")
		}
		if seen[NCRRefused] && seen[NCRApproved] {
			t.Fatal("an NCR was both refused and approved")
		}
	})
}
