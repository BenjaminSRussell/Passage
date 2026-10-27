package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to WOStatus
		want     bool
	}{
		{WOUnscheduled, WOScheduled, true},
		{WOScheduled, WODispatched, true},
		{WOInProgress, WOOnHold, true},
		{WOOnHold, WOInProgress, true},
		{WOInProgress, WOCompleted, true},
		{WOCompleted, WOPosted, true},
		{WOCompleted, WOCanceled, false},
		{WOPosted, WOCanceled, false},
		{WOUnscheduled, WOCompleted, false},
		{WOTraveling, WOCanceled, true},
		{WOScheduled, WOInProgress, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// AC-45.6: a work order with a warranty-covered part and 3 labor hours under an
// agreement with 1 included hour posts 2 billable labor hours, a zero-priced
// part with warranty cost, and consumes 1 entitlement hour.
func TestPost_AC456WarrantyAndEntitlement(t *testing.T) {
	in := PostInput{
		Status:   WOCompleted,
		Currency: "USD",
		Lines: []WorkLine{
			{ID: "part-1", Kind: KindPart, Qty: dec("1"), UnitPrice: dec("220.00"), UnitCost: dec("180.00")},
			{ID: "labor-1", Kind: KindLabor, Qty: dec("3"), UnitPrice: dec("95.00"), UnitCost: dec("40.00")},
		},
		Warranty:  WarrantyCover{Active: true, Parts: true},
		Agreement: &AgreementCover{Labor: true, HoursRemaining: dec("1")},
	}
	got, err := Post(in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.BillableTotal.Equal(dec("190.00")) {
		t.Errorf("billable total = %s, want 190.00 (2 labor hours at 95)", got.BillableTotal)
	}
	if !got.WarrantyCost.Equal(dec("180.00")) {
		t.Errorf("warranty cost = %s, want 180.00", got.WarrantyCost)
	}
	if !got.EntitlementConsumed.Equal(dec("1")) {
		t.Errorf("entitlement consumed = %s, want 1", got.EntitlementConsumed)
	}
	var billableQty, agreementQty decimal.Decimal
	for _, l := range got.Lines {
		if l.Kind != KindLabor {
			continue
		}
		switch l.Coverage {
		case CoverBillable:
			billableQty = billableQty.Add(l.Qty)
		case CoverAgreement:
			agreementQty = agreementQty.Add(l.Qty)
		}
	}
	if !billableQty.Equal(dec("2")) || !agreementQty.Equal(dec("1")) {
		t.Errorf("labor split billable=%s agreement=%s, want 2 and 1", billableQty, agreementQty)
	}
	for _, l := range got.Lines {
		if l.Kind == KindPart && (l.Coverage != CoverWarranty || !l.Amount.IsZero()) {
			t.Errorf("part line %+v must be warranty and zero-priced", l)
		}
	}
}

// Invariant: invoiced amount equals the sum of BILLABLE lines.
func TestPost_BillableTotalIsSumOfBillableLines(t *testing.T) {
	got, err := Post(PostInput{Status: WOCompleted, Currency: "USD", Lines: []WorkLine{
		{ID: "a", Kind: KindTravel, Qty: dec("1"), UnitPrice: dec("12.345")},
		{ID: "b", Kind: KindFee, Qty: dec("2"), UnitPrice: dec("9.99")},
		{ID: "c", Kind: KindPart, Qty: dec("3"), UnitPrice: dec("1.10")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, l := range got.Lines {
		if l.Coverage == CoverBillable {
			sum = sum.Add(l.Amount)
		}
	}
	if !sum.Equal(got.BillableTotal) {
		t.Fatalf("sum of billable lines %s != total %s", sum, got.BillableTotal)
	}
	// 12.345 rounds half-even to 12.34 at 2 decimals; 9.99*2 = 19.98; 3.30.
	if !got.BillableTotal.Equal(dec("12.34").Add(dec("19.98")).Add(dec("3.30"))) {
		t.Fatalf("total = %s", got.BillableTotal)
	}
}

func TestPost_AgreementCoverageWithoutHoursIsBillable(t *testing.T) {
	got, err := Post(PostInput{Status: WOCompleted, Currency: "USD", Agreement: &AgreementCover{Labor: true, HoursRemaining: decimal.Zero},
		Lines: []WorkLine{{ID: "l", Kind: KindLabor, Qty: dec("2"), UnitPrice: dec("50")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.BillableTotal.Equal(dec("100")) || !got.EntitlementConsumed.IsZero() {
		t.Fatalf("exhausted hours must bill: total=%s consumed=%s", got.BillableTotal, got.EntitlementConsumed)
	}
}

func TestPost_RequiresCompleted(t *testing.T) {
	_, err := Post(PostInput{Status: WOInProgress, Currency: "USD"})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeNotCompleted {
		t.Fatalf("err = %v, want %s", err, CodeNotCompleted)
	}
}

func TestWarrantyActive(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		on       time.Time
		excluded bool
		want     bool
	}{
		{"on the start date", start, false, true},
		{"before start", start.AddDate(0, 0, -1), false, false},
		{"last day of 12 months", start.AddDate(1, 0, -1), false, true},
		{"term ended", start.AddDate(1, 0, 0), false, false},
		{"excluded item", start, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WarrantyActive(start, 12, tc.on, tc.excluded); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
