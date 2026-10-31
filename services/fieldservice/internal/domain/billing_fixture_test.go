package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// BillingFixture is svc-billing-30 (testdata/svc-billing-30): the work orders
// as written and the postings reference_billing.py derived for them
// independently of this package. The store test posts the same orders.
type BillingFixture struct {
	Agreements map[string]struct {
		Parts         bool            `json:"parts"`
		Labor         bool            `json:"labor"`
		Travel        bool            `json:"travel"`
		IncludedHours decimal.Decimal `json:"includedHours"`
	} `json:"agreements"`
	WorkOrders []BillingCase `json:"workOrders"`
}

// BillingCase is one completed work order of the fixture.
type BillingCase struct {
	ID          string `json:"id"`
	Currency    string `json:"currency"`
	Agreement   string `json:"agreement"`
	CompletedOn string `json:"completedOn"`
	Warranty    *struct {
		StartsOn string `json:"startsOn"`
		Months   int    `json:"months"`
		Parts    bool   `json:"parts"`
		Labor    bool   `json:"labor"`
		Travel   bool   `json:"travel"`
	} `json:"warranty"`
	Lines []WorkLine `json:"lines"`
}

// CompletedDay is the completion day of the order (2026-10-21 unless given).
func (b BillingCase) CompletedDay(t *testing.T) time.Time {
	t.Helper()
	day, err := time.Parse(DateLayout, orDefault(b.CompletedOn, "2026-10-21"))
	if err != nil {
		t.Fatal(err)
	}
	return day
}

// WarrantyTerm and start of the order's service object, or nil start without one.
func (b BillingCase) WarrantyTerm(t *testing.T) (WarrantyTerm, *time.Time) {
	t.Helper()
	if b.Warranty == nil {
		return WarrantyTerm{}, nil
	}
	start, err := time.Parse(DateLayout, b.Warranty.StartsOn)
	if err != nil {
		t.Fatal(err)
	}
	return WarrantyTerm{StartTrigger: StartShip, Months: b.Warranty.Months, PartsCovered: b.Warranty.Parts,
		LaborCovered: b.Warranty.Labor, TravelCovered: b.Warranty.Travel}, &start
}

// BillingAnswer is the reference posting; lines are [lineId, coverage, qty, amount, warrantyCost, agreementHours].
type BillingAnswer struct {
	BillableTotal       decimal.Decimal `json:"billableTotal"`
	WarrantyCost        decimal.Decimal `json:"warrantyCost"`
	EntitlementConsumed decimal.Decimal `json:"entitlementConsumed"`
	Lines               [][6]string     `json:"lines"`
}

// LoadBillingFixture reads cases.json and expected.json from dir.
func LoadBillingFixture(t *testing.T, dir string) (BillingFixture, map[string]BillingAnswer) {
	t.Helper()
	var fx BillingFixture
	var want map[string]BillingAnswer
	for name, into := range map[string]any{"cases.json": &fx, "expected.json": &want} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(fx.WorkOrders) != 30 || len(want) != 30 {
		t.Fatalf("fixture has %d work orders and %d answers, want 30 of each", len(fx.WorkOrders), len(want))
	}
	return fx, want
}

// CheckPosting compares a posting with the reference answer, line by line
// (keyed by line id and coverage, so the order of posted lines is free) and
// by its three totals. It reports every difference.
func CheckPosting(t *testing.T, got Posting, want BillingAnswer) {
	t.Helper()
	if !got.BillableTotal.Equal(want.BillableTotal) || !got.WarrantyCost.Equal(want.WarrantyCost) || !got.EntitlementConsumed.Equal(want.EntitlementConsumed) {
		t.Errorf("totals billable %s warranty %s consumed %s, want %s %s %s", got.BillableTotal, got.WarrantyCost, got.EntitlementConsumed,
			want.BillableTotal, want.WarrantyCost, want.EntitlementConsumed)
	}
	wantLines := map[string][6]string{}
	for _, l := range want.Lines {
		wantLines[l[0]+"/"+l[1]] = l
	}
	if len(got.Lines) != len(wantLines) {
		t.Errorf("%d posted lines, want %d: %+v", len(got.Lines), len(wantLines), got.Lines)
	}
	for _, l := range got.Lines {
		key := l.LineID + "/" + string(l.Coverage)
		w, ok := wantLines[key]
		if !ok {
			t.Errorf("unexpected posted line %s: %+v", key, l)
			continue
		}
		for i, v := range []decimal.Decimal{l.Qty, l.Amount, l.WarrantyCost, l.AgreementHours} {
			if !v.Equal(decimal.RequireFromString(w[2+i])) {
				t.Errorf("line %s = qty %s amount %s warrantyCost %s hours %s, want %v", key, l.Qty, l.Amount, l.WarrantyCost, l.AgreementHours, w[2:])
				break
			}
		}
	}
}

// Ticket AC3 (domain): each svc-billing-30 order posts the reference lines and
// totals for billable, warranty and agreement coverage; included hours carry
// over between the orders that share an agreement, as the store's usage does.
func TestPost_SvcBilling30(t *testing.T) {
	fx, want := LoadBillingFixture(t, filepath.Join("testdata", "svc-billing-30"))
	remaining := map[string]decimal.Decimal{}
	for label, a := range fx.Agreements {
		remaining[label] = a.IncludedHours
	}
	for _, b := range fx.WorkOrders {
		t.Run(b.ID, func(t *testing.T) {
			term, start := b.WarrantyTerm(t)
			in := PostInput{Status: WOCompleted, Currency: b.Currency, Lines: b.Lines, Warranty: WarrantyCoverOn(term, start, b.CompletedDay(t))}
			if b.Agreement != "" {
				a := fx.Agreements[b.Agreement]
				in.Agreement = &AgreementCover{Parts: a.Parts, Labor: a.Labor, Travel: a.Travel, HoursRemaining: remaining[b.Agreement]}
			}
			got, err := Post(in)
			if err != nil {
				t.Fatal(err)
			}
			if b.Agreement != "" {
				remaining[b.Agreement] = remaining[b.Agreement].Sub(got.EntitlementConsumed)
			}
			CheckPosting(t, got, want[b.ID])
		})
	}
}
