package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func TestApprovalChain_MostSeniorCategoryWins(t *testing.T) {
	a := New(nil)
	tests := []struct {
		name  string
		total string
		cats  []string
		want  []string
	}{
		{"auto band stays empty", "500", []string{"MRO"}, nil},
		{"manager band", "5000", []string{"MRO"}, []string{domain.RoleManager}},
		{"category owner joins above 10k", "50000", []string{"MRO", "IT"}, []string{domain.RoleManager, domain.RoleCategoryOwner}},
		{"CFO above 100k", "250000", []string{"IT"}, []string{domain.RoleManager, domain.RoleCategoryOwner, domain.RoleCFO}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := store.Requisition{Total: decimal.RequireFromString(tc.total)}
			for _, c := range tc.cats {
				r.Lines = append(r.Lines, store.RequisitionLine{Category: c})
			}
			if got := a.approvalChain(r); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("chain = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildRequisition_LineCap(t *testing.T) {
	body := requisitionBody{LegalEntityID: ids.New(), Currency: "USD"}
	for i := 0; i <= maxRequisitionLines; i++ {
		body.Lines = append(body.Lines, requisitionLineBody{ProductID: ids.New(), Qty: "1", Unit: "EA", RequestedDate: "2026-11-01"})
	}
	_, _, err := buildRequisition(body, ids.New())
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != codeTooManyLines {
		t.Fatalf("err = %v, want %s", err, codeTooManyLines)
	}
}

func TestBuildRequisition_AmountRoundsOnce(t *testing.T) {
	body := requisitionBody{LegalEntityID: ids.New(), Currency: "USD", Lines: []requisitionLineBody{
		{ProductID: ids.New(), Qty: "3", Unit: "EA", EstimatedUnitPrice: "1.005", RequestedDate: "2026-11-01"},
	}}
	out, needs, err := buildRequisition(body, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if needs[0] {
		t.Fatal("a supplied estimate must not be defaulted")
	}
	if got := out.Total.String(); got != "3.02" {
		t.Fatalf("total = %s, want 3.02 (3 × 1.005 = 3.015 rounds half-even to 3.02)", got)
	}
}

func TestBuildAgreement_RejectsUnknownLevelsAndInverseValidity(t *testing.T) {
	ok := agreementBody{
		Relation: domain.RelationPrice, AccountCode: domain.AccountAll, ItemCode: domain.ItemAll,
		Currency: "USD", Unit: "EA", Price: "9.50", ValidFrom: mustDay(t, "2026-01-01"),
	}
	if _, err := buildAgreement(ok); err != nil {
		t.Fatalf("valid row rejected: %v", err)
	}
	vendorNoID := ok
	vendorNoID.AccountCode = domain.AccountVendor
	if _, err := buildAgreement(vendorNoID); err == nil {
		t.Fatal("vendor-level row without accountId must be rejected")
	}
	to := mustDay(t, "2025-12-31")
	inverted := ok
	inverted.ValidTo = &to
	if _, err := buildAgreement(inverted); err == nil {
		t.Fatal("validTo before validFrom must be rejected")
	}
}

func mustDay(t *testing.T, s string) domain.Day {
	t.Helper()
	d, err := domain.ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEstimatePrices_FallsBackToTheLastOrderPrice(t *testing.T) {
	product := ids.New()
	last := &store.PriceHistoryEntry{Number: "PO-00000042", OrderDate: mustDay(t, "2026-02-03"), UnitPrice: decimal.RequireFromString("1250"),
		PriceUnit: decimal.NewFromInt(100), DiscountPct: decimal.NewFromInt(10)}
	tests := []struct {
		name      string
		line      string
		last      *store.PriceHistoryEntry
		wantPrice string
		wantTotal string
		wantCode  string
		wantCalls int
	}{
		{"net of discount per price unit", `{"productId":"` + product + `","qty":"3","unit":"EA","requestedDate":"2026-03-10"}`, last, "11.25", "33.75", codeLastPrice, 1},
		{"no earlier order keeps zero and warns", `{"productId":"` + product + `","qty":"3","unit":"EA","requestedDate":"2026-03-10"}`, nil, "0", "0", codeNoEstimate, 1},
		{"a given estimate is never replaced", `{"productId":"` + product + `","qty":"3","unit":"EA","estimatedUnitPrice":"2","requestedDate":"2026-03-10"}`, last, "2", "6", "", 0},
		{"a non-catalog line has no history", `{"description":"Chairs","category":"OFFICE","qty":"3","unit":"EA","requestedDate":"2026-03-10"}`, last, "0", "0", codeNoEstimate, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.lastPrice = tc.last
			body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[` + tc.line + `]}`
			rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: body, headers: idem("k-last")})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var out store.Requisition
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if !out.Lines[0].EstimatedUnitPrice.Equal(decimal.RequireFromString(tc.wantPrice)) || !out.Total.Equal(decimal.RequireFromString(tc.wantTotal)) {
				t.Fatalf("estimate %s total %s, want %s and %s", out.Lines[0].EstimatedUnitPrice, out.Total, tc.wantPrice, tc.wantTotal)
			}
			if tc.wantCode == "" && len(out.Warnings) != 0 || tc.wantCode != "" && (len(out.Warnings) != 1 || out.Warnings[0].Code != tc.wantCode) {
				t.Fatalf("warnings = %+v, want %q", out.Warnings, tc.wantCode)
			}
			if h.fake.lastPriceCalls != tc.wantCalls {
				t.Fatalf("last price lookups = %d, want %d", h.fake.lastPriceCalls, tc.wantCalls)
			}
		})
	}
}

func TestEstimatePrices_LastPriceLookupFailureFailsTheCreate(t *testing.T) {
	h := newHarness(t)
	h.fake.lastPriceErr = errors.New("db down")
	body := `{"legalEntityId":"` + leA + `","currency":"USD","lines":[{"productId":"` + ids.New() + `","qty":"1","unit":"EA","requestedDate":"2026-03-10"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions", user: userA, duties: []string{dutyRequisitionCreate}, body: body, headers: idem("k-err")})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body)
	}
}
