package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestValidateCampaign(t *testing.T) {
	ok := Campaign{Name: "Spring", ValidFrom: msDay("2026-03-01"), ValidTo: msDay("2026-03-31"), Budget: decimal.NewFromInt(5000), Currency: "USD"}
	cases := []struct {
		name   string
		mut    func(c *Campaign)
		fields []string
	}{
		{"valid campaign", func(*Campaign) {}, nil},
		{"one-day campaign", func(c *Campaign) { c.ValidTo = c.ValidFrom }, nil},
		{"zero budget", func(c *Campaign) { c.Budget = decimal.Zero }, nil},
		{"empty name", func(c *Campaign) { c.Name = "" }, []string{"name"}},
		{"long name", func(c *Campaign) { c.Name = strings.Repeat("n", 121) }, []string{"name"}},
		{"validTo before validFrom", func(c *Campaign) { c.ValidTo = msDay("2026-02-28") }, []string{"validity"}},
		{"missing validity", func(c *Campaign) { c.ValidFrom = time.Time{} }, []string{"validity"}},
		{"negative budget", func(c *Campaign) { c.Budget = decimal.RequireFromString("-0.01") }, []string{"budget"}},
		{"lower-case currency", func(c *Campaign) { c.Currency = "usd" }, []string{"currency"}},
		{"long target group", func(c *Campaign) { c.TargetCustomerGroup = strings.Repeat("g", 65) }, []string{"targetCustomerGroup"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.mut(&c)
			assertFieldKeys(t, ValidateCampaign(c), tc.fields)
		})
	}
}

func TestCampaignLiveAndTargets(t *testing.T) {
	base := Campaign{ValidFrom: msDay("2026-03-01"), ValidTo: msDay("2026-03-31")}
	killed := base
	killed.Killed = true
	cases := []struct {
		name string
		c    Campaign
		day  string
		want bool
	}{
		{"first day inclusive", base, "2026-03-01", true},
		{"last day inclusive", base, "2026-03-31", true},
		{"before validity", base, "2026-02-28", false},
		{"after validity", base, "2026-04-01", false},
		{"kill switch overrides validity", killed, "2026-03-15", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CampaignLive(tc.c, msDay(tc.day)); got != tc.want {
				t.Fatalf("CampaignLive = %v, want %v", got, tc.want)
			}
		})
	}
	targeted := Campaign{TargetCustomerGroup: "RETAIL"}
	if !CampaignTargets(base, "") || !CampaignTargets(base, "WHOLESALE") {
		t.Fatal("a campaign without a target group applies to every customer")
	}
	if !CampaignTargets(targeted, "RETAIL") || CampaignTargets(targeted, "WHOLESALE") || CampaignTargets(targeted, "") {
		t.Fatal("a targeted campaign applies only to customers of its group")
	}
}

func procStep(n int, ct ConditionType) ProcedureStep {
	return ProcedureStep{StepNo: n, ConditionType: ct}
}

func TestValidateSteps(t *testing.T) {
	reference := []ProcedureStep{procStep(10, TypePrice), procStep(20, TypeLineDisc), procStep(30, TypeMultiline),
		procStep(40, TypeTotalDisc), procStep(50, TypeSurcharge)}
	grouped := func(n int, ct ConditionType, group, mode string) ProcedureStep {
		return ProcedureStep{StepNo: n, ConditionType: ct, ExclusionGroup: group, ExclusionMode: mode}
	}
	tooMany := make([]ProcedureStep, MaxProcedureSteps+1)
	for i := range tooMany {
		tooMany[i] = procStep(10+i, TypeLineDisc)
	}
	cases := []struct {
		name   string
		steps  []ProcedureStep
		fields []string
	}{
		{"reference procedure", reference, nil},
		{"every condition type", append(append([]ProcedureStep{}, reference...), procStep(60, TypeFreight),
			procStep(70, TypeRebateAccrual), procStep(80, TypeFreeGoods), procStep(990, TypeRounding)), nil},
		{"exclusion group BEST over two discounts", []ProcedureStep{procStep(10, TypePrice),
			grouped(20, TypeLineDisc, "EG1", ExclusionBest), grouped(30, TypeMultiline, "EG1", ExclusionBest)}, nil},
		{"no steps", nil, []string{"steps"}},
		{"too many steps", tooMany, []string{"steps"}},
		{"step below 10", []ProcedureStep{procStep(5, TypePrice)}, []string{"steps[0].stepNo"}},
		{"step above 990", []ProcedureStep{procStep(991, TypePrice)}, []string{"steps[0].stepNo"}},
		{"repeated step number", []ProcedureStep{procStep(10, TypePrice), procStep(10, TypeLineDisc)}, []string{"steps[1].stepNo"}},
		{"descending steps", []ProcedureStep{procStep(20, TypePrice), procStep(10, TypeLineDisc)}, []string{"steps[1].stepNo"}},
		{"unknown type", []ProcedureStep{procStep(10, TypePrice), procStep(20, "BOGUS")}, []string{"steps[1].conditionType"}},
		{"no PRICE step", []ProcedureStep{procStep(10, TypeLineDisc)}, []string{"steps"}},
		{"two PRICE steps", []ProcedureStep{procStep(10, TypePrice), procStep(20, TypePrice)}, []string{"steps"}},
		{"requirement over 512 characters", []ProcedureStep{{StepNo: 10, ConditionType: TypePrice, RequirementCEL: strings.Repeat("x", 513)}},
			[]string{"steps[0].requirementCel"}},
		{"requirement of 512 characters", []ProcedureStep{{StepNo: 10, ConditionType: TypePrice, RequirementCEL: strings.Repeat("x", 512)}}, nil},
		{"long account key", []ProcedureStep{{StepNo: 10, ConditionType: TypePrice, AccountKey: strings.Repeat("A", 33)}},
			[]string{"steps[0].accountKey"}},
		{"group without mode", []ProcedureStep{procStep(10, TypePrice), grouped(20, TypeLineDisc, "EG1", "")},
			[]string{"steps[1].exclusionMode"}},
		{"mode without group", []ProcedureStep{procStep(10, TypePrice), grouped(20, TypeLineDisc, "", ExclusionFirst)},
			[]string{"steps[1].exclusionMode"}},
		{"mixed modes in one group", []ProcedureStep{procStep(10, TypePrice), grouped(20, TypeLineDisc, "EG1", ExclusionBest),
			grouped(30, TypeMultiline, "EG1", ExclusionFirst)}, []string{"steps[2].exclusionMode"}},
		{"PRICE in a group", []ProcedureStep{grouped(10, TypePrice, "EG1", ExclusionBest)}, []string{"steps[0].exclusionGroup"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFieldKeys(t, ValidateSteps(tc.steps), tc.fields)
		})
	}
}

func TestValidateProcedureName(t *testing.T) {
	for _, ok := range []string{"AX_STANDARD", "SAP_RVAA01", "US-RETAIL"} {
		if msg := ValidateProcedureName(ok); msg != "" {
			t.Errorf("%q rejected: %s", ok, msg)
		}
	}
	for _, bad := range []string{"", "ax_standard", "_LEADING", "WITH SPACE", strings.Repeat("A", 65)} {
		if ValidateProcedureName(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProcedureImmutableIsPRC0008(t *testing.T) {
	err := ProcedureImmutable(Procedure{Name: "AX_STANDARD", Version: 3, Status: ProcedureActive})
	var re *RuleError
	if !errors.As(err, &re) || re.Code != "PRC-0008" || re.Status != 409 || !strings.Contains(re.Message, "version 3 is ACTIVE") {
		t.Fatalf("immutable error = %#v", err)
	}
}

// assertFieldKeys checks that a validation result has exactly the wanted field keys.
func assertFieldKeys(t *testing.T, got map[string]string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("fields = %v, want keys %v", got, want)
	}
	for _, f := range want {
		if _, has := got[f]; !has {
			t.Fatalf("missing field error %q in %v", f, got)
		}
	}
}

// TestResolve_CampaignRecordMatchesMoreDimensions covers BR-26.1 step 4 for the campaign
// dimension: at one access and break, a campaign record outranks a plain record even when
// its price is higher, and records of different campaigns do not overlap.
func TestResolve_CampaignRecordMatchesMoreDimensions(t *testing.T) {
	plain := Record{ID: "R-PLAIN", Status: StatusActive, Type: TypePrice, CustomerKind: CustomerKindAll, ProductKind: ProductKindProduct,
		ProductKey: "P1", Currency: "USD", Unit: "EA", Amount: decimal.RequireFromString("9.00"),
		ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
	promo := plain
	promo.ID, promo.CampaignID, promo.Amount = "R-PROMO", "CMP-1", decimal.RequireFromString("9.50")
	res := Resolve(Input{Header: Header{CustomerID: "C1", Currency: "USD", PricingDate: msDay("2026-03-10")},
		Lines: []Line{{No: 1, ProductID: "P1", Unit: "EA", Quantity: decimal.NewFromInt(2)}}, Records: []Record{plain, promo}})
	if got := res.Lines[0]; got.UnitPrice.String() != "9.5" || got.Applied[0].RecordID != "R-PROMO" {
		t.Fatalf("line = %+v; want the campaign record at 9.50", got)
	}
	if _, clash := Conflict(promo, []Record{plain}); clash {
		t.Fatal("a campaign record must not overlap the plain record of the same key")
	}
	other := promo
	other.ID = "R-PROMO-2"
	if c, clash := Conflict(other, []Record{promo}); !clash || c.ID != "R-PROMO" {
		t.Fatal("two records of one campaign with the same key must overlap")
	}
}

// TestValidateJournal_KeepsTheCampaign: an UPDATE line carries a full after-record without a
// campaign, but the record stays in its campaign, so its key (and overlap bucket) is unchanged.
func TestValidateJournal_KeepsTheCampaign(t *testing.T) {
	promo := jRec("r1", "C1", "10")
	promo.CampaignID = "CMP-1"
	plain := jRec("r2", "C1", "11")
	j := Journal{Lines: []JournalLine{{No: 1, Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, Record: jRec("", "C1", "9")}}}
	if !ValidateJournal(&j, jStored(promo, plain), DefaultSmartBands()) {
		t.Fatalf("journal = %+v; want clean (the plain record of the same key is not in the campaign)", j.Lines[0].Errors)
	}
	if after := j.Lines[0].After; after.CampaignID != "CMP-1" || after.Amount.String() != "9" {
		t.Fatalf("after = %+v; want amount 9 in campaign CMP-1", after)
	}
}
