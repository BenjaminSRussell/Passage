package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func ruleCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func validTxn() Transaction {
	worker := "0b4a4c43-6f3c-4c55-9f7c-2f7f5d1c0a11"
	return Transaction{ProjectID: "ES-1001", TxnType: TxnHour, CategoryID: "LABOR", LineProperty: LineChargeable, TxnDate: "2026-09-10",
		Quantity: "8", CostPrice: "50.00", SalesPrice: "90.00", Currency: "USD", WorkerID: &worker, SourceDocType: SourceManual}
}

func TestValidateTransaction_TableDriven(t *testing.T) {
	item := "6f0e1a52-3d1b-4b4a-8f60-0a9f3a4c2b10"
	cases := []struct {
		name   string
		mutate func(*Transaction)
		field  string
	}{
		{"valid", func(*Transaction) {}, ""},
		{"unknown type", func(x *Transaction) { x.TxnType = "PARTS" }, "txnType"},
		{"bad property", func(x *Transaction) { x.LineProperty = "BOTH" }, "lineProperty"},
		{"empty category", func(x *Transaction) { x.CategoryID = "" }, "categoryId"},
		{"long category", func(x *Transaction) { x.CategoryID = "123456789012345678901" }, "categoryId"},
		{"lower-case category", func(x *Transaction) { x.CategoryID = "labor" }, "categoryId"},
		{"bad date", func(x *Transaction) { x.TxnDate = "10/09/2026" }, "txnDate"},
		{"bad currency", func(x *Transaction) { x.Currency = "usd" }, "currency"},
		{"negative quantity", func(x *Transaction) { x.Quantity = "-1" }, "quantity"},
		{"quantity over 3 places", func(x *Transaction) { x.Quantity = "1.2345" }, "quantity"},
		{"quantity not a number", func(x *Transaction) { x.Quantity = "eight" }, "quantity"},
		{"quantity too large", func(x *Transaction) { x.Quantity = "100000000000" }, "quantity"},
		{"negative cost", func(x *Transaction) { x.CostPrice = "-0.01" }, "costPrice"},
		{"cost over 6 places", func(x *Transaction) { x.CostPrice = "1.0000001" }, "costPrice"},
		{"negative sales on hours", func(x *Transaction) { x.SalesPrice = "-5" }, "salesPrice"},
		{"negative sales on account is a retainer", func(x *Transaction) { x.TxnType = TxnOnAccount; x.WorkerID = nil; x.SalesPrice = "-5" }, ""},
		{"hour without worker", func(x *Transaction) { x.WorkerID = nil }, "workerId"},
		{"item without item id", func(x *Transaction) { x.TxnType = TxnItem }, "itemId"},
		{"item with item id", func(x *Transaction) { x.TxnType = TxnItem; x.ItemID = &item }, ""},
		{"unknown source", func(x *Transaction) { x.SourceDocType = "CARRIER_PIGEON" }, "sourceDocType"},
		{"long description", func(x *Transaction) { x.Description = strings.Repeat("é", 501) }, "description"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := validTxn()
			c.mutate(&x)
			fields := ValidateTransaction(x)
			if c.field == "" {
				if len(fields) != 0 {
					t.Fatalf("expected valid, got %v", fields)
				}
				return
			}
			if _, ok := fields[c.field]; !ok || len(fields) != 1 {
				t.Fatalf("expected exactly an error on %s, got %v", c.field, fields)
			}
		})
	}
}

// TestPriceLine_RoundsEachLineHalfEven is BR-41.5: amount = qty × price
// rounded half-even to the currency's minor unit, per line.
func TestPriceLine_RoundsEachLineHalfEven(t *testing.T) {
	cases := []struct {
		currency, qty, cost, sales, wantCost, wantSales string
	}{
		{"USD", "8", "50.00", "90.00", "400.00", "720.00"},
		{"USD", "1.5", "33.335", "1", "50.00", "1.50"},    // 50.0025 → 50.00
		{"USD", "1", "0.125", "0.375", "0.12", "0.38"},    // half-even on the minor unit
		{"JPY", "3", "1000.5", "2000.5", "3002", "6002"},  // zero-decimal currency: 3001.5 → 3002, 6001.5 → 6002
		{"USD", "2", "0", "-2500.00", "0.00", "-5000.00"}, // retainer applied
	}
	for _, c := range cases {
		x := validTxn()
		x.Currency, x.Quantity, x.CostPrice, x.SalesPrice = c.currency, c.qty, c.cost, c.sales
		if err := PriceLine(&x); err != nil {
			t.Fatal(err)
		}
		if x.CostAmount != c.wantCost || x.SalesAmount != c.wantSales {
			t.Errorf("%s %s × (%s, %s) = (%s, %s), want (%s, %s)", c.currency, c.qty, c.cost, c.sales, x.CostAmount, x.SalesAmount, c.wantCost, c.wantSales)
		}
	}
	x := validTxn()
	x.Quantity, x.CostPrice = "1.5", "2"
	if err := PriceLine(&x); err != nil || x.Quantity != "1.500" || x.CostPrice != "2.000000" {
		t.Fatalf("normalised decimals = %q %q (%v), want 1.500 and 2.000000", x.Quantity, x.CostPrice, err)
	}
}

// TestLineAmount_SumOfLinesIsNotRoundedTotal shows why BR-41.5 rounds per line:
// three lines of 0.005 each round to 0.00 (half-even), not 0.02 for the total.
func TestLineAmount_SumOfLinesIsNotRoundedTotal(t *testing.T) {
	sum := decimal.Zero
	for i := 0; i < 3; i++ {
		sum = sum.Add(LineAmount(decimal.RequireFromString("1"), decimal.RequireFromString("0.005"), "USD"))
	}
	if !sum.Equal(decimal.Zero) {
		t.Fatalf("sum of rounded lines = %s, want 0", sum)
	}
}

// TestLineAmount_WithinHalfMinorUnit: the rounded amount never differs from the
// exact product by more than half a minor unit, and always has the minor
// unit's scale.
func TestLineAmount_WithinHalfMinorUnit(t *testing.T) {
	half := decimal.RequireFromString("0.005")
	rapid.Check(t, func(t *rapid.T) {
		qty := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "qty"), -3)
		price := decimal.New(rapid.Int64Range(-1_000_000_000, 1_000_000_000).Draw(t, "price"), -6)
		got := LineAmount(qty, price, "USD")
		if diff := got.Sub(qty.Mul(price)).Abs(); diff.GreaterThan(half) {
			t.Fatalf("%s × %s rounded to %s, off by %s", qty, price, got, diff)
		}
		if got.Exponent() < -2 {
			t.Fatalf("%s has more than 2 places", got)
		}
	})
}

func TestCheckTransactionProject_StageAndCurrency(t *testing.T) {
	x := validTxn()
	for _, s := range []Stage{StageCreated, StageOnHold, StageFinished} {
		if got := ruleCode(CheckTransactionProject(s, "USD", x)); got != CodeTxnStage {
			t.Errorf("stage %s: code %q, want %s", s, got, CodeTxnStage)
		}
	}
	if got := ruleCode(CheckTransactionProject(StageInProcess, "EUR", x)); got != CodeTxnCurrency {
		t.Errorf("currency mismatch: code %q, want %s", got, CodeTxnCurrency)
	}
	if err := CheckTransactionProject(StageInProcess, "USD", x); err != nil {
		t.Errorf("IN_PROCESS in project currency: %v", err)
	}
}

func TestCheckCategory_ExistsAndTypeBound(t *testing.T) {
	x := validTxn()
	if got := ruleCode(CheckCategory(false, "", x)); got != CodeTxnCategory {
		t.Errorf("missing category: %q", got)
	}
	if got := ruleCode(CheckCategory(true, TxnCost, x)); got != CodeTxnCategory {
		t.Errorf("COST category on an HOUR line: %q", got)
	}
	if err := CheckCategory(true, TxnHour, x); err != nil {
		t.Errorf("matching category: %v", err)
	}
	if got := ruleCode(CheckCategoryType(TxnHour, TxnCost, "LABOR")); got != CodeCategoryType {
		t.Errorf("type change: %q", got)
	}
	if err := CheckCategoryType(TxnHour, TxnHour, "LABOR"); err != nil {
		t.Errorf("same type: %v", err)
	}
}

func TestValidateCategory(t *testing.T) {
	if f := ValidateCategory(Category{ID: "LABOR", Name: "Labour", TxnType: TxnHour}); len(f) != 0 {
		t.Fatalf("valid category: %v", f)
	}
	f := ValidateCategory(Category{ID: "la bor", Name: "", TxnType: "PARTS"})
	for _, k := range []string{"id", "name", "txnType"} {
		if _, ok := f[k]; !ok {
			t.Errorf("expected an error on %s, got %v", k, f)
		}
	}
}

func TestCheckProposalMove_StateMachine(t *testing.T) {
	cases := []struct {
		from, to ProposalStatus
		lines    int
		code     string
	}{
		{ProposalDraft, ProposalApproved, 2, ""},
		{ProposalDraft, ProposalApproved, 0, CodeProposalEmpty},
		{ProposalDraft, ProposalCanceled, 0, ""},
		{ProposalApproved, ProposalCanceled, 1, ""},
		{ProposalApproved, ProposalApproved, 1, CodeProposalState},
		{ProposalApproved, ProposalPosted, 1, CodeProposalState}, // posting is the wave-29 hand-off
		{ProposalCanceled, ProposalApproved, 1, CodeProposalState},
		{ProposalCanceled, ProposalCanceled, 1, CodeProposalState},
		{ProposalPosted, ProposalCanceled, 1, CodeProposalState},
	}
	for _, c := range cases {
		if got := ruleCode(CheckProposalMove(c.from, c.to, c.lines)); got != c.code {
			t.Errorf("%s → %s (%d lines): code %q, want %q", c.from, c.to, c.lines, got, c.code)
		}
	}
}

func TestCheckLineRemoval_ReturnsToDraftUntilPosted(t *testing.T) {
	for _, s := range []ProposalStatus{ProposalDraft, ProposalApproved} {
		next, err := CheckLineRemoval(s)
		if err != nil || next != ProposalDraft {
			t.Errorf("%s: next %s, err %v; want DRAFT", s, next, err)
		}
	}
	for _, s := range []ProposalStatus{ProposalPosted, ProposalCanceled} {
		if _, err := CheckLineRemoval(s); ruleCode(err) != CodeProposalState {
			t.Errorf("%s: err %v, want %s", s, err, CodeProposalState)
		}
	}
}

func TestValidateProposalRequest(t *testing.T) {
	ok := ProposalRequest{ProjectID: "ES-1001", From: "2026-09-01", To: "2026-09-30", TxnTypes: []TxnType{TxnHour}}
	if f := ValidateProposalRequest(ok); len(f) != 0 {
		t.Fatalf("valid request: %v", f)
	}
	cases := []struct {
		name   string
		mutate func(*ProposalRequest)
		field  string
	}{
		{"bad project", func(r *ProposalRequest) { r.ProjectID = "es 1" }, "projectId"},
		{"bad from", func(r *ProposalRequest) { r.From = "2026-9-1" }, "from"},
		{"to before from", func(r *ProposalRequest) { r.To = "2026-08-31" }, "to"},
		{"unknown type", func(r *ProposalRequest) { r.TxnTypes = []TxnType{"PARTS"} }, "txnTypes"},
		{"duplicate type", func(r *ProposalRequest) { r.TxnTypes = []TxnType{TxnHour, TxnHour} }, "txnTypes"},
	}
	for _, c := range cases {
		r := ok
		c.mutate(&r)
		if _, found := ValidateProposalRequest(r)[c.field]; !found {
			t.Errorf("%s: expected an error on %s", c.name, c.field)
		}
	}
}

func TestBillableTypes_ByProjectType(t *testing.T) {
	all, err := BillableTypes(TypeTimeMaterial, nil)
	if err != nil || !slices.Equal(all, TxnTypes) {
		t.Fatalf("T&M with no types = %v, %v; want every type", all, err)
	}
	some, err := BillableTypes(TypeTimeMaterial, []TxnType{TxnFee, TxnHour})
	if err != nil || !slices.Equal(some, []TxnType{TxnHour, TxnFee}) {
		t.Fatalf("T&M HOUR+FEE = %v, %v; want [HOUR FEE] in display order", some, err)
	}
	fp, err := BillableTypes(TypeFixedPrice, nil)
	if err != nil || !slices.Equal(fp, []TxnType{TxnOnAccount}) || !BillsMilestones(TypeFixedPrice) {
		t.Fatalf("fixed price = %v, %v; want ON_ACCOUNT milestones", fp, err)
	}
	if _, err := BillableTypes(TypeFixedPrice, []TxnType{TxnHour}); ruleCode(err) != CodeProposalType {
		t.Fatalf("fixed price HOUR: %v, want %s", err, CodeProposalType)
	}
	for _, pt := range []ProjectType{TypeInternal, TypeCost, TypeInvestment, TypeTime} {
		if _, err := BillableTypes(pt, nil); ruleCode(err) != CodeProposalType {
			t.Errorf("%s: %v, want %s", pt, err, CodeProposalType)
		}
		if BillsMilestones(pt) {
			t.Errorf("%s bills milestones", pt)
		}
	}
	if BillsMilestones(TypeTimeMaterial) {
		t.Error("T&M bills milestones")
	}
}

func TestEmptySelection_NamesTheCause(t *testing.T) {
	err := EmptySelection("ES-1", StageOnHold, TypeTimeMaterial, []TxnType{TxnHour})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeProposalEmpty || !strings.Contains(de.Detail, "ON_HOLD") {
		t.Fatalf("on hold: %v", err)
	}
	err = EmptySelection("ES-1", StageInProcess, TypeFixedPrice, []TxnType{TxnOnAccount})
	if !errors.As(err, &de) || !strings.Contains(de.Detail, "milestone") {
		t.Fatalf("fixed price: %v", err)
	}
}
