package domain_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func calc(kind, margin string, rounding int) domain.CommodityCalc {
	return domain.CommodityCalc{LegalEntityID: "le", Name: "Cu", BasisID: "b", Factor: dec("0.0012"), MarginKind: kind, Margin: dec(margin),
		Rounding: rounding, MaxIndexAgeDays: 7, CustomerKind: domain.CustomerKindAll, ProductID: "P1", Unit: "EA", Currency: "USD"}
}

func priceRecord(id, amount string) domain.Record {
	return domain.Record{ID: id, Version: 3, Status: domain.StatusActive, Type: domain.TypePrice, CustomerKind: domain.CustomerKindAll,
		ProductKind: domain.ProductKindProduct, ProductKey: "P1", Unit: "EA", Currency: "USD", Amount: dec(amount),
		ValidFrom: day("2026-01-01"), ValidTo: day("2026-12-31")}
}

func TestCalcPrice(t *testing.T) {
	cases := []struct {
		kind, margin string
		rounding     int
		index, want  string
	}{
		{domain.MarginPercent, "10", 2, "9100", "12.012"},            // round(10.92, 2) + 10% = 12.012 (6 decimals)
		{domain.MarginAmount, "1", 2, "9000", "11.8"},                // 10.80 + 1
		{domain.MarginAmount, "0", 2, "9000", "10.8"},                // no margin
		{domain.MarginPercent, "0", 2, "8333.33", "10"},              // 9.999996 -> 10.00
		{domain.MarginAmount, "0.005", 2, "1000", "1.205"},           // the amount margin is added after rounding and kept
		{domain.MarginAmount, "0", 2, "1000.5", "1.2"},               // 1.2006 -> 1.20: only index x factor takes the 2 decimals
		{domain.MarginAmount, "0", 3, "1001.25", "1.202"},            // 1.2015 -> half to even 1.202
		{domain.MarginAmount, "0", 3, "1003.75", "1.204"},            // 1.2045 -> half to even 1.204
		{domain.MarginAmount, "0", 3, "1000.416667", "1.201"},        // 1.2005000004 is above the half
		{domain.MarginPercent, "33.333333", 2, "1000", "1.6"},        // 1.20 x 1.33333333 = 1.599999996 -> 1.600000 at 6 decimals
		{domain.MarginPercent, "2.5", 6, "9100.123456", "11.193152"}, // 10.9201481472 -> 10.920148, +2.5% = 11.193152
	}
	for _, c := range cases {
		got := domain.CalcPrice(calc(c.kind, c.margin, c.rounding), dec(c.index))
		if want := dec(c.want); !got.Equal(want) {
			t.Errorf("CalcPrice(%s %s, r%d, index %s) = %s; want %s", c.kind, c.margin, c.rounding, c.index, got, want)
		}
	}
}

func TestPlanRun_ChangesOnlyTargetedRecordsAndFlagsStaleIndex(t *testing.T) {
	c := calc(domain.MarginPercent, "10", 2)
	other := priceRecord("r-other", "9")
	other.CustomerKind, other.CustomerKey = domain.CustomerKindCustomer, "C1"
	broken := priceRecord("r-break", "9")
	broken.FromQuantity = dec("10")
	campaign := priceRecord("r-camp", "9")
	campaign.CampaignID = "camp"
	expired := priceRecord("r-old", "9")
	expired.ValidTo = day("2026-03-31")
	draft := priceRecord("r-draft", "9")
	draft.Status = domain.StatusDraft
	same := priceRecord("r-same", "12.012")
	stored := []domain.Record{priceRecord("r-1", "9"), same, other, broken, campaign, expired, draft}
	idx := domain.IndexValue{BasisID: "b", AsOf: day("2026-05-25"), Revision: 2, Value: dec("9100")}

	plan, err := domain.PlanRun(c, idx, day("2026-06-10"), stored)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Price.Equal(dec("12.012")) || len(plan.Changes) != 1 || plan.Changes[0].Record.ID != "r-1" || plan.Unchanged != 1 {
		t.Fatalf("plan = %+v; want one change to r-1 and one unchanged", plan)
	}
	if !plan.Stale || plan.AgeDays != 16 {
		t.Fatalf("stale = %v age %d; want stale at 16 days", plan.Stale, plan.AgeDays)
	}
	lines := plan.JournalLines()
	if len(lines) != 1 || lines[0].Op != domain.OpUpdate || lines[0].RecordID != "r-1" || lines[0].ExpectedVersion != 3 || !lines[0].Record.Amount.Equal(dec("12.012")) {
		t.Fatalf("journal lines = %+v", lines)
	}
	if again, _ := domain.PlanRun(c, idx, day("2026-06-10"), stored); len(again.Changes) != 1 || !again.Price.Equal(plan.Price) {
		t.Fatalf("PlanRun is not deterministic: %+v", again)
	}
	fresh, _ := domain.PlanRun(c, domain.IndexValue{AsOf: day("2026-06-03"), Value: dec("9100")}, day("2026-06-10"), stored)
	if fresh.Stale {
		t.Fatalf("a 7 day old index is within the window of 7 days")
	}
}

func TestPlanRun_RefusesPriceThatRoundsToNothing(t *testing.T) {
	c := calc(domain.MarginAmount, "0", 2)
	_, err := domain.PlanRun(c, domain.IndexValue{AsOf: day("2026-06-10"), Value: dec("1")}, day("2026-06-10"), nil)
	if err == nil || err.Code != domain.CodePriceNotPositive {
		t.Fatalf("err = %v; want PRC-0053", err)
	}
	// A price a NUMERIC(19,6) unit price cannot hold is refused before anything is written.
	big := c
	big.Factor = dec("9999999999999")
	if _, err := domain.PlanRun(big, domain.IndexValue{AsOf: day("2026-06-10"), Value: dec("2")}, day("2026-06-10"), nil); err == nil || err.Code != domain.CodePriceNotPositive {
		t.Fatalf("oversized price err = %v; want PRC-0053", err)
	}
}

func TestValidateCommodity(t *testing.T) {
	if f := domain.ValidateBasis(domain.CommodityBasis{Code: "cu lme", Currency: "usd"}); len(f) != 4 {
		t.Errorf("basis errors = %v; want code, name, currency, unit", f)
	}
	if f := domain.ValidateBasis(domain.CommodityBasis{Code: "CU-LME", Name: "Copper", Currency: "USD", Unit: "MT"}); len(f) != 0 {
		t.Errorf("valid basis errors = %v", f)
	}
	for _, v := range []domain.IndexValue{{AsOf: day("2026-01-01"), Value: dec("0")}, {AsOf: day("2026-01-01"), Value: dec("1.1234567")}, {Value: dec("1")}} {
		if f := domain.ValidateIndexValue(v); len(f) == 0 {
			t.Errorf("index %+v accepted", v)
		}
	}
	if f := domain.ValidateIndexValue(domain.IndexValue{AsOf: day("2026-01-01"), Value: dec("9100.5")}); len(f) != 0 {
		t.Errorf("valid index errors = %v", f)
	}
	good := calc(domain.MarginPercent, "10", 2)
	if f := domain.ValidateCalc(good); len(f) != 0 {
		t.Errorf("valid calculation errors = %v", f)
	}
	bad := good
	bad.Factor, bad.MarginKind, bad.Rounding, bad.MaxIndexAgeDays, bad.CustomerKind, bad.Currency = dec("0"), "X", 7, 0, domain.CustomerKindCustomer, "usd"
	if f := domain.ValidateCalc(bad); len(f) != 6 {
		t.Errorf("bad calculation errors = %v; want 6", f)
	}
	huge := good
	huge.Factor, huge.MarginKind, huge.Margin = dec("10000000000000"), domain.MarginAmount, dec("10000000000000")
	if f := domain.ValidateCalc(huge); f["factor"] == "" || f["margin"] == "" {
		t.Errorf("factor and margin beyond NUMERIC(19,6) accepted: %v", f)
	}
	pct := good
	pct.Margin = dec("1001")
	if f := domain.ValidateCalc(pct); f["margin"] == "" {
		t.Errorf("1001 percent accepted")
	}
}
