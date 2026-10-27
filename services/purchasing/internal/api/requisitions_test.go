package api

import (
	"errors"
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
