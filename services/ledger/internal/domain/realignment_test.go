package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateRealignment(t *testing.T) {
	attrs := []string{"DEPT", "PROJECT"}
	ok := RealignmentFilter{AccountFrom: "6000", AccountTo: "6999", PeriodFrom: "2026-01", PeriodTo: "2026-03"}
	good := RealignmentMapping{"DEPT": {"C100": "C150"}}
	if p := ValidateRealignment(ok, good, attrs); len(p) != 0 {
		t.Fatalf("valid run refused: %v", p)
	}
	cases := []struct {
		name   string
		f      RealignmentFilter
		m      RealignmentMapping
		fields string
	}{
		{"bad period", RealignmentFilter{PeriodFrom: "2026-13", PeriodTo: "2026-03"}, good, "filter.periodFrom"},
		{"reversed periods", RealignmentFilter{PeriodFrom: "2026-05", PeriodTo: "2026-03"}, good, "filter.periodTo"},
		{"too many periods", RealignmentFilter{PeriodFrom: "2020-01", PeriodTo: "2026-03"}, good, "filter.periodTo"},
		{"reversed accounts", RealignmentFilter{AccountFrom: "7", AccountTo: "6", PeriodFrom: "2026-01", PeriodTo: "2026-01"}, good, "filter.accountTo"},
		{"no mapping", ok, nil, "mapping"},
		{"unknown attribute", ok, RealignmentMapping{"COLOR": {"A": "B"}}, "mapping.COLOR"},
		{"identity", ok, RealignmentMapping{"DEPT": {"A": "A"}}, "mapping.DEPT"},
		{"bad value", ok, RealignmentMapping{"DEPT": {"A": "has space"}}, "mapping.DEPT"},
		{"empty attribute", ok, RealignmentMapping{"DEPT": {}}, "mapping.DEPT"},
	}
	for _, c := range cases {
		if p := ValidateRealignment(c.f, c.m, attrs); p[c.fields] == "" {
			t.Errorf("%s: no problem on %s: %v", c.name, c.fields, p)
		}
	}
}

func TestPlanRealignment_MovesOnlyChangedNonZeroBalances(t *testing.T) {
	m := RealignmentMapping{"DEPT": {"C100": "C150"}}
	cands := []RealignmentCandidate{
		{AccountID: "a1", Account: "6010", Period: "2026-02", Dimensions: map[string]string{"DEPT": "C100"}, Net: decimal.RequireFromString("40.00"), LineCount: 2, DisplayName: "6010-C100"},
		{AccountID: "a1", Account: "6010", Period: "2026-01", Dimensions: map[string]string{"DEPT": "C100", "PROJECT": "P1"}, Net: decimal.RequireFromString("-15.50"), LineCount: 1, DisplayName: "6010-C100-P1"},
		{AccountID: "a1", Account: "6010", Period: "2026-01", Dimensions: map[string]string{"DEPT": "C100"}, Net: decimal.Zero, LineCount: 4},
		{AccountID: "a1", Account: "6010", Period: "2026-01", Dimensions: map[string]string{"DEPT": "C200"}, Net: decimal.RequireFromString("9"), LineCount: 1},
	}
	rows := PlanRealignment(m, cands, func(a string, d map[string]string) string { return a + "-" + d["DEPT"] })
	if len(rows) != 2 || rows[0].Period != "2026-01" || rows[1].Period != "2026-02" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].NewDims["DEPT"] != "C150" || rows[0].NewDims["PROJECT"] != "P1" || rows[0].OldDims["DEPT"] != "C100" {
		t.Errorf("mapping not applied: %+v", rows[0])
	}
	moved, lines := RealignmentTotals(rows)
	if moved.String() != "55.5" || lines != 3 {
		t.Errorf("totals = %s, %d", moved, lines)
	}
}

func TestRealignmentLines_BalanceAndFollowTheNetSign(t *testing.T) {
	rows := []RealignmentRow{
		{Account: "6010", OldDims: map[string]string{"DEPT": "C100"}, NewDims: map[string]string{"DEPT": "C150"}, Net: "40.00"},
		{Account: "4010", OldDims: map[string]string{"DEPT": "C100"}, NewDims: map[string]string{"DEPT": "C150"}, Net: "-15.50"},
	}
	lines := RealignmentLines(rows, "USD", "t")
	if len(lines) != 4 {
		t.Fatalf("lines = %d", len(lines))
	}
	if lines[0].Side != SideCredit || lines[1].Side != SideDebit || lines[2].Side != SideDebit || lines[3].Side != SideCredit {
		t.Errorf("sides: %v %v %v %v", lines[0].Side, lines[1].Side, lines[2].Side, lines[3].Side)
	}
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		if l.Side == SideDebit {
			debit = debit.Add(l.Amount)
		} else {
			credit = credit.Add(l.Amount)
		}
	}
	if !debit.Equal(credit) || !strings.HasPrefix(lines[2].Amount.String(), "15.5") {
		t.Errorf("not balanced: %s %s", debit, credit)
	}
}

func TestPeriodHelpers(t *testing.T) {
	if n, _ := PeriodSpan("2026-11", "2027-02"); n != 4 {
		t.Errorf("span = %d", n)
	}
	if end, _ := PeriodEnd("2028-02"); end.Format("2006-01-02") != "2028-02-29" {
		t.Errorf("end = %s", end)
	}
}

func TestCheckRealignmentReason(t *testing.T) {
	ledgerReason := &ReasonInfo{Code: "REORG", AppliesTo: []string{ModuleLedger}, Active: true}
	cases := []struct {
		name          string
		reason        *ReasonInfo
		code, comment string
		want          []string
	}{
		{"missing", nil, "", "", []string{"LEDGER_REASON_REQUIRED:reasonCode"}},
		{"unknown", nil, "NOPE", "", []string{"LEDGER_REASON_REQUIRED:reasonCode"}},
		{"inactive", &ReasonInfo{Code: "OLD", AppliesTo: []string{ModuleLedger}}, "OLD", "", []string{"LEDGER_REASON_REQUIRED:reasonCode"}},
		{"other module", &ReasonInfo{Code: "BANK", AppliesTo: []string{"bank"}, Active: true}, "BANK", "x", []string{"LEDGER_REASON_INVALID_MODULE:reasonCode"}},
		{"comment required", &ReasonInfo{Code: "WHY", AppliesTo: []string{ModuleLedger}, RequiresComment: true, Active: true}, "WHY", "  ",
			[]string{"LEDGER_REASON_REQUIRED:reasonComment"}},
		{"valid", ledgerReason, "REORG", "", nil},
	}
	for _, c := range cases {
		var got []string
		for _, v := range CheckRealignmentReason(c.reason, c.code, c.comment) {
			got = append(got, v.Code+":"+v.Field)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestChunkRealignmentRows(t *testing.T) {
	rows := make([]RealignmentRow, 5)
	for i := range rows {
		rows[i].Account = fmt.Sprint(i)
	}
	chunks := ChunkRealignmentRows(rows, 2)
	if len(chunks) != 3 || len(chunks[0]) != 2 || len(chunks[2]) != 1 || chunks[2][0].Account != "4" {
		t.Fatalf("chunks of 2: %v", chunks)
	}
	if got := ChunkRealignmentRows(rows, 0); len(got) != 1 || len(got[0]) != 5 {
		t.Fatalf("default size: %v", got)
	}
	if got := ChunkRealignmentRows(nil, 2); len(got) != 0 {
		t.Fatalf("no rows: %v", got)
	}
	if MaxRealignmentRowsPerEntry*2 != MaxLinesPerEntry {
		t.Fatalf("an entry of %d rows has %d lines, the limit is %d", MaxRealignmentRowsPerEntry, MaxRealignmentRowsPerEntry*2, MaxLinesPerEntry)
	}
}
