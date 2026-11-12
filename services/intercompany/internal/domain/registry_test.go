package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func usd(v string) money.Amount {
	return money.Amount{Value: decimal.RequireFromString(v), Currency: "USD"}
}

func TestChargeJournalWithTax_EachEntityBalancesAndTheTaxSitsInTheDueAccounts(t *testing.T) {
	m := Matrix{{EntityA: "A", EntityB: "B", DueFrom: "1500", DueTo: "2500"}}
	pricing := ChargePricing{Cost: usd("500.00"), Price: usd("550.00"), Margin: usd("50.00")}
	j, err := ChargeJournalWithTax(m, "p1", "A", "B", "6200", "4900", "4910", pricing, ChargeTax{Amount: decimal.RequireFromString("104.50"), Code: "DE-VAT", LenderAccount: "2200", BorrowerAccount: "1400"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Balanced(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range j.Legs {
		got[l.Entity+l.Account+string(l.Side)] = l.Amount.Value.StringFixed(2)
	}
	want := map[string]string{"B6200debit": "550.00", "B1400debit": "104.50", "B2500credit": "654.50", "A1500debit": "654.50", "A4900credit": "500.00", "A4910credit": "50.00", "A2200credit": "104.50"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %s (legs %v)", k, got[k], v, got)
		}
	}
	// No tax is the plain journal; tax without accounts is refused with its own code.
	plain, err := ChargeJournalWithTax(m, "p1", "A", "B", "6200", "4900", "4910", pricing, ChargeTax{})
	if err != nil || len(plain.Legs) != 5 {
		t.Fatalf("plain = %d legs, %v", len(plain.Legs), err)
	}
	if _, err := ChargeJournalWithTax(m, "p1", "A", "B", "6200", "4900", "4910", pricing, ChargeTax{Amount: decimal.NewFromInt(5)}); err == nil || err.(*Error).Code != CodeNoTaxAccount {
		t.Fatalf("no accounts: %v", err)
	}
}

func TestValidateMatrix(t *testing.T) {
	reg := map[string]bool{"A": true, "B": true}
	ok := Matrix{{EntityA: "A", EntityB: "B", DueFrom: "1", DueTo: "2"}, {EntityA: "B", EntityB: "A", DueFrom: "1", DueTo: "2"}}
	if err := ValidateMatrix(ok, reg); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Matrix{
		"itself":  {{EntityA: "A", EntityB: "A", DueFrom: "1", DueTo: "2"}},
		"unknown": {{EntityA: "A", EntityB: "Z", DueFrom: "1", DueTo: "2"}},
		"twice":   {{EntityA: "A", EntityB: "B", DueFrom: "1", DueTo: "2"}, {EntityA: "A", EntityB: "B", DueFrom: "3", DueTo: "4"}},
		"empty":   {{EntityA: "A", EntityB: "B", DueFrom: "", DueTo: "2"}},
	} {
		if err := ValidateMatrix(m, reg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestTransferPriceAndEntityValidation(t *testing.T) {
	good := TransferPrice{Lender: "A", Borrower: "B", Category: "Hour", Method: TPCostPlus, MarkupPct: decimal.NewFromInt(5), Currency: "USD", EffectiveFrom: time.Now()}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.MarkupPct = decimal.Zero
	if bad.Validate() == nil {
		t.Error("a CostPlus price without a markup must be refused")
	}
	bad = good
	bad.Method, bad.UnitPrice = TPTable, decimal.Zero
	if bad.Validate() == nil {
		t.Error("a Table price without a unit price must be refused")
	}
	e := EntityRecord{Entity: "A", LegalEntityID: "x", LedgerID: "L", Currency: "USD", Country: "us"}
	if e.Validate() == nil {
		t.Error("a lower-case country must be refused")
	}
	e.Country = "US"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if !CrossBorder(EntityRecord{Country: "US"}, EntityRecord{Country: "DE"}) || CrossBorder(EntityRecord{Country: "US"}, EntityRecord{}) || CrossBorder(EntityRecord{Country: "US"}, EntityRecord{Country: "US"}) {
		t.Error("CrossBorder compares two known, different countries only")
	}
}

func TestClassifyRunCountsAndOpensTheItemsThatNeedAction(t *testing.T) {
	run, items := ClassifyRun(ReconRun{Period: "2026-11", Currency: "USD", FXTolerancePct: DefaultFXTolerancePct}, []ReconItem{
		{Entity: "A", Counterparty: "B", AmountA: decimal.NewFromInt(100), AmountB: decimal.NewFromInt(100)},
		{Entity: "A", Counterparty: "C", AmountA: decimal.NewFromInt(100), AmountB: decimal.NewFromInt(60)},
	})
	if run.Matched != 1 || run.Mismatch != 1 || items[0].Resolution != ResolutionNone || items[1].Resolution != ResolutionOpen {
		t.Fatalf("run = %+v items = %+v", run, items)
	}
}
