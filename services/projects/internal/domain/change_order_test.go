package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func approved(lines ...ChangeOrderLine) ChangeOrder {
	return ChangeOrder{Number: "CO-0001", Type: COScopeChange, Status: COStatusApproved, CreatedBy: "pm", Lines: lines}
}

func line(wbs *string, cost, revenue, effort string, days int) ChangeOrderLine {
	return ChangeOrderLine{LineNo: 1, WBSCode: wbs, CostImpact: cost, RevenueImpact: revenue, EffortHoursDelta: effort, ScheduleDaysDelta: days}
}

func TestValidateChangeOrder(t *testing.T) {
	node := "00000000-0000-0000-0000-000000000001"
	ok := ChangeOrderLine{CostImpact: "-1250.50", RevenueImpact: "0", EffortHoursDelta: "0"}
	tests := []struct {
		name  string
		co    ChangeOrder
		field string
	}{
		{"valid scope change", ChangeOrder{Type: COScopeChange, Title: "Panel", Lines: []ChangeOrderLine{ok}}, ""},
		{"unknown type", ChangeOrder{Type: "CLAIM", Title: "x", Lines: []ChangeOrderLine{ok}}, "type"},
		{"title too long", ChangeOrder{Type: COInternal, Title: string(make([]rune, 121)), Lines: []ChangeOrderLine{ok}}, "title"},
		{"vendor claim without vendor", ChangeOrder{Type: COClaimVendor, Title: "x", Lines: []ChangeOrderLine{ok}}, "vendorPartyId"},
		{"vendor on a scope change", ChangeOrder{Type: COScopeChange, Title: "x", VendorPartyID: ptr(node), Lines: []ChangeOrderLine{ok}}, "vendorPartyId"},
		{"vendor not a uuid", ChangeOrder{Type: COClaimVendor, Title: "x", VendorPartyID: ptr("acme"), Lines: []ChangeOrderLine{ok}}, "vendorPartyId"},
		{"no lines", ChangeOrder{Type: COInternal, Title: "x"}, "lines"},
		{"exponent amount", ChangeOrder{Type: COInternal, Title: "x", Lines: []ChangeOrderLine{{CostImpact: "1e3", RevenueImpact: "0", EffortHoursDelta: "0"}}}, "lines[0].costImpact"},
		{"too many fraction digits", ChangeOrder{Type: COInternal, Title: "x", Lines: []ChangeOrderLine{{CostImpact: "0", RevenueImpact: "1.0000001", EffortHoursDelta: "0"}}}, "lines[0].revenueImpact"},
		{"effort without node", ChangeOrder{Type: COInternal, Title: "x", Lines: []ChangeOrderLine{{CostImpact: "0", RevenueImpact: "0", EffortHoursDelta: "-8"}}}, "lines[0].wbsNodeId"},
		{"schedule without node", ChangeOrder{Type: COInternal, Title: "x", Lines: []ChangeOrderLine{{CostImpact: "0", RevenueImpact: "0", EffortHoursDelta: "0", ScheduleDaysDelta: 3}}}, "lines[0].wbsNodeId"},
		{"effort with node", ChangeOrder{Type: COInternal, Title: "x", Lines: []ChangeOrderLine{{WBSNodeID: ptr(node), CostImpact: "0", RevenueImpact: "0", EffortHoursDelta: "-8.25"}}}, ""},
		{"empty approval ref", ChangeOrder{Type: COClaimCustomer, Title: "x", CustomerApprovalRef: ptr(""), Lines: []ChangeOrderLine{ok}}, "customerApprovalRef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := ValidateChangeOrder(tt.co)
			if tt.field == "" && len(fields) > 0 {
				t.Fatalf("unexpected errors %v", fields)
			}
			if tt.field != "" && fields[tt.field] == "" {
				t.Fatalf("want an error on %s, got %v", tt.field, fields)
			}
		})
	}
}

func TestRoundLinesUsesTheCurrencyMinorUnit(t *testing.T) {
	lines, err := RoundLines([]ChangeOrderLine{
		{CostImpact: "10.005", RevenueImpact: "10.015", EffortHoursDelta: "1.5"},
		{CostImpact: "1234.5", RevenueImpact: "-0.5", EffortHoursDelta: "0"},
	}, "USD")
	if err != nil {
		t.Fatal(err)
	}
	// Half-even: 10.005 → 10.00, 10.015 → 10.02.
	if lines[0].CostImpact != "10.00" || lines[0].RevenueImpact != "10.02" || lines[0].EffortHoursDelta != "1.50" || lines[1].LineNo != 2 {
		t.Fatalf("USD rounding: %+v", lines)
	}
	jpy, _ := RoundLines([]ChangeOrderLine{{CostImpact: "1234.5", RevenueImpact: "1235.5", EffortHoursDelta: "0"}}, "JPY")
	if jpy[0].CostImpact != "1234" || jpy[0].RevenueImpact != "1236" {
		t.Fatalf("JPY rounding: %+v", jpy[0])
	}
}

func TestLifecycleMoves(t *testing.T) {
	tests := []struct {
		from ChangeOrderStatus
		to   ChangeOrderStatus
		want string
	}{
		{COStatusDraft, COStatusSubmitted, ""},
		{COStatusDraft, COStatusApproved, CodeChangeOrderState},
		{COStatusSubmitted, COStatusApproved, ""},
		{COStatusSubmitted, COStatusRejected, ""},
		{COStatusSubmitted, COStatusApplied, CodeChangeOrderState},
		{COStatusApproved, COStatusApplied, ""},
		{COStatusApproved, COStatusRejected, CodeChangeOrderState},
		{COStatusRejected, COStatusSubmitted, CodeChangeOrderState},
		{COStatusApplied, COStatusApplied, CodeChangeOrderApplied},
		{COStatusApplied, COStatusSubmitted, CodeChangeOrderApplied},
	}
	for _, tt := range tests {
		got := code(CheckChangeOrderMove(ChangeOrder{Status: tt.from}, tt.to))
		if got != tt.want {
			t.Errorf("%s → %s: got %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestCheckDecision(t *testing.T) {
	submitted := ChangeOrder{Number: "CO-0001", Type: COScopeChange, Status: COStatusSubmitted, CreatedBy: "pm"}
	claim := submitted
	claim.Type = COClaimCustomer
	withRef := claim
	withRef.CustomerApprovalRef = ptr("Letter 42")
	tests := []struct {
		name     string
		co       ChangeOrder
		to       ChangeOrderStatus
		actor    string
		required bool
		want     string
	}{
		{"creator submits", ChangeOrder{Status: COStatusDraft, CreatedBy: "pm"}, COStatusSubmitted, "pm", true, ""},
		{"creator approves", submitted, COStatusApproved, "pm", true, CodeSelfApproval},
		{"anonymous approves", submitted, COStatusApproved, "", true, CodeSelfApproval},
		{"creator rejects", submitted, COStatusRejected, "pm", true, CodeSelfApproval},
		{"other approves", submitted, COStatusApproved, "ctl", true, ""},
		{"claim without evidence", claim, COStatusApproved, "ctl", true, CodeCustomerApproval},
		{"claim without evidence, parameter off", claim, COStatusApproved, "ctl", false, ""},
		{"claim rejected without evidence", claim, COStatusRejected, "ctl", true, ""},
		{"claim with evidence", withRef, COStatusApproved, "ctl", true, ""},
		{"move checked first", submitted, COStatusApplied, "ctl", true, CodeChangeOrderState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := code(CheckDecision(tt.co, tt.to, tt.actor, tt.required)); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func baselineNodes() []WBSNode {
	root, effort := "n1", "120.00"
	return []WBSNode{
		{ID: "n1", WBSCode: "1", Name: "Build"},
		{ID: "n11", WBSCode: "1.1", ParentID: &root, Name: "Design", DurationDays: 5},
		{ID: "n13", WBSCode: "1.3", ParentID: &root, Name: "Install", DurationDays: 4, EffortHours: &effort},
	}
}

// TestApplyChangeOrder_AC4112 is the arithmetic of PRD AC-41.12.
func TestApplyChangeOrder_AC4112(t *testing.T) {
	base := baselineNodes()
	co := approved(line(ptr("1.3"), "30000.00", "50000.00", "40.00", 2))
	got, err := ApplyChangeOrder(co, "500000.000000", "USD", base)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContractValueDelta != "50000.00" || got.BudgetDelta != "30000.00" || got.ContractValue != "550000.00" ||
		got.EffortHoursDelta != "40.00" || got.ScheduleDaysDelta != 2 {
		t.Fatalf("effect: %+v", got)
	}
	n := got.Nodes[2]
	if n.DurationDays != 6 || *n.EffortHours != "160.00" {
		t.Fatalf("1.3 after apply: %+v", n)
	}
	if base[2].DurationDays != 4 || *base[2].EffortHours != "120.00" {
		t.Fatal("ApplyChangeOrder modified the current baseline")
	}
}

func TestApplyChangeOrder_Refusals(t *testing.T) {
	base := baselineNodes()
	tests := []struct {
		name     string
		co       ChangeOrder
		contract string
		want     string
	}{
		{"not approved", ChangeOrder{Status: COStatusSubmitted}, "0", CodeChangeOrderState},
		{"already applied", ChangeOrder{Status: COStatusApplied}, "0", CodeChangeOrderApplied},
		{"negative contract", approved(line(nil, "0", "-100.01", "0", 0)), "100.00", CodeApplyImpact},
		{"code not in baseline", approved(line(ptr("2"), "0", "0", "8", 0)), "0", CodeApplyImpact},
		{"negative effort", approved(line(ptr("1.3"), "0", "0", "-120.01", 0)), "0", CodeApplyImpact},
		{"negative duration", approved(line(ptr("1.1"), "0", "0", "0", -6)), "0", CodeApplyImpact},
		{"duration on a summary", approved(line(ptr("1"), "0", "0", "0", 1)), "0", CodeApplyImpact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ApplyChangeOrder(tt.co, tt.contract, "USD", base); code(err) != tt.want {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
	// A saving down to exactly zero is allowed.
	if got, err := ApplyChangeOrder(approved(line(nil, "-5", "-100.00", "0", 0)), "100", "USD", base); err != nil || got.ContractValue != "0.00" || got.BudgetDelta != "-5.00" {
		t.Fatalf("saving to zero: %+v %v", got, err)
	}
}

// TestApplyChangeOrder_DeltasAreLineSums checks with random lines that the
// contract and budget deltas are exactly Σ revenue and Σ cost impact, the new
// contract value is old + Σ revenue, and the node durations move by the sum of
// their lines' schedule deltas.
func TestApplyChangeOrder_DeltasAreLineSums(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "lines")
		lines := make([]ChangeOrderLine, n)
		cost, revenue := decimal.Zero, decimal.Zero
		days := 0
		for i := range lines {
			c := decimal.New(rapid.Int64Range(-1_000_000, 1_000_000).Draw(t, "cost"), -2)
			r := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "revenue"), -2)
			d := rapid.IntRange(0, 5).Draw(t, "days")
			cost, revenue, days = cost.Add(c), revenue.Add(r), days+d
			lines[i] = ChangeOrderLine{LineNo: i + 1, WBSCode: ptr("1.1"), CostImpact: c.StringFixed(2), RevenueImpact: r.StringFixed(2), EffortHoursDelta: "0", ScheduleDaysDelta: d}
		}
		contract := decimal.New(rapid.Int64Range(0, 1_000_000_000).Draw(t, "contract"), -2)
		got, err := ApplyChangeOrder(approved(lines...), contract.String(), "USD", baselineNodes())
		if err != nil {
			t.Fatal(err)
		}
		if got.BudgetDelta != cost.StringFixed(2) || got.ContractValueDelta != revenue.StringFixed(2) ||
			got.ContractValue != contract.Add(revenue).StringFixed(2) || got.Nodes[1].DurationDays != 5+days {
			t.Fatalf("effect %+v for cost %s revenue %s days %d", got, cost, revenue, days)
		}
		if c, r, _ := Totals(lines, "USD"); c != got.BudgetDelta || r != got.ContractValueDelta {
			t.Fatalf("Totals disagrees with ApplyChangeOrder: %s/%s", c, r)
		}
	})
}

func TestRiskRegisterRules(t *testing.T) {
	valid := RiskIssue{Kind: KindRisk, Title: "Steel", Probability: 5, Impact: 5}
	if f := ValidateRiskIssue(valid); len(f) != 0 || RiskScore(5, 5) != 25 || RiskScore(1, 1) != 1 {
		t.Fatalf("valid entry: %v", f)
	}
	for _, tt := range []struct {
		mutate func(*RiskIssue)
		field  string
	}{
		{func(r *RiskIssue) { r.Kind = "RUMOUR" }, "kind"},
		{func(r *RiskIssue) { r.Probability = 0 }, "probability"},
		{func(r *RiskIssue) { r.Impact = 6 }, "impact"},
		{func(r *RiskIssue) { r.Title = "" }, "title"},
		{func(r *RiskIssue) { r.DueDate = ptr("2026-13-01") }, "dueDate"},
		{func(r *RiskIssue) { r.OwnerWorkerID = ptr("bob") }, "ownerWorkerId"},
		{func(r *RiskIssue) { r.Status = "DONE" }, "status"},
	} {
		r := valid
		tt.mutate(&r)
		if ValidateRiskIssue(r)[tt.field] == "" {
			t.Errorf("want an error on %s", tt.field)
		}
	}
	moves := map[[2]RiskStatus]bool{
		{RiskOpen, RiskOpen}: true, {RiskOpen, RiskMitigated}: true, {RiskOpen, RiskClosed}: true,
		{RiskMitigated, RiskOpen}: true, {RiskMitigated, RiskClosed}: true,
		{RiskClosed, RiskClosed}: false, {RiskClosed, RiskOpen}: false, {RiskClosed, RiskMitigated}: false,
	}
	for m, allowed := range moves {
		err := CheckRiskMove(m[0], m[1])
		if allowed != (err == nil) || (!allowed && code(err) != CodeRiskStatus) {
			t.Errorf("%s → %s: err %v, allowed %v", m[0], m[1], err, allowed)
		}
	}
}

func TestRoundContractValue(t *testing.T) {
	for _, tt := range []struct{ in, cur, want string }{{"", "USD", "0.00"}, {"500000", "USD", "500000.00"}, {"0.125", "EUR", "0.12"}, {"99.5", "JPY", "100"}} {
		got, err := RoundContractValue(tt.in, tt.cur)
		if err != nil || got != tt.want {
			t.Errorf("%s %s: got %s %v, want %s", tt.in, tt.cur, got, err, tt.want)
		}
	}
	if f := ValidateProject(Project{ID: "P1", ProjectGroupID: "ENG", ProjectType: TypeInternal, Currency: "USD", StartDate: "2026-01-01", ContractValue: "-1"}); f["contractValue"] == "" {
		t.Fatalf("negative contract value accepted: %v", f)
	}
}
