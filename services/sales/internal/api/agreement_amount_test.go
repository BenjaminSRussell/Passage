package api

import (
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// TestBuildAgreement_Amount covers the commitment type of FR-27.10: an AMOUNT
// agreement takes commitmentAmount (positive, at most 4 places) at a price above
// zero and refuses commitmentQty; a QUANTITY agreement refuses commitmentAmount.
func TestBuildAgreement_Amount(t *testing.T) {
	good := createAgreementRequest{LegalEntityID: ids.New(), CustomerID: ids.New(), ProductID: ids.New(), Unit: "EA", UnitPrice: "12.50",
		Currency: "USD", CommitmentType: "AMOUNT", CommitmentAmount: "25000.50"}
	tests := []struct {
		name      string
		mutate    func(*createAgreementRequest)
		wantField string
	}{
		{name: "valid"},
		{name: "amount missing", mutate: func(r *createAgreementRequest) { r.CommitmentAmount = "" }, wantField: "commitmentAmount"},
		{name: "zero amount", mutate: func(r *createAgreementRequest) { r.CommitmentAmount = "0" }, wantField: "commitmentAmount"},
		{name: "five places", mutate: func(r *createAgreementRequest) { r.CommitmentAmount = "1.00001" }, wantField: "commitmentAmount"},
		{name: "quantity with an amount type", mutate: func(r *createAgreementRequest) { r.CommitmentQty = "10" }, wantField: "commitmentQty"},
		{name: "free goods cannot draw an amount", mutate: func(r *createAgreementRequest) { r.UnitPrice = "0" }, wantField: "unitPrice"},
		{name: "unknown type", mutate: func(r *createAgreementRequest) { r.CommitmentType = "VALUE" }, wantField: "commitmentType"},
		{name: "amount with a quantity type", mutate: func(r *createAgreementRequest) { r.CommitmentType, r.CommitmentQty = "QUANTITY", "10" }, wantField: "commitmentAmount"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := good
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			in, fields := buildAgreement(req)
			if tc.wantField == "" {
				if len(fields) != 0 || in.CommitmentType != "AMOUNT" || !in.Commitment.Equal(decimal.RequireFromString("25000.50")) {
					t.Fatalf("fields %v type %s commitment %s", fields, in.CommitmentType, in.Commitment)
				}
				return
			}
			if _, ok := fields[tc.wantField]; !ok {
				t.Fatalf("want field error on %q, got %v", tc.wantField, fields)
			}
		})
	}
	quantity := good
	quantity.CommitmentType, quantity.CommitmentAmount, quantity.CommitmentQty = "", "", "100"
	if in, fields := buildAgreement(quantity); len(fields) != 0 || in.CommitmentType != "QUANTITY" {
		t.Fatalf("the default type is QUANTITY: %v %s", fields, in.CommitmentType)
	}
}

// TestReviseAgreement_Amount: PATCH takes commitmentAmount, marks it for the
// store, and refuses both commitment fields together.
func TestReviseAgreement_Amount(t *testing.T) {
	path := agreements + "/" + ids.New()
	p := principal(dutyAgreement)
	s := &lifecycleStore{}
	hdr := map[string]string{"If-Match": `"2"`}
	for _, body := range []string{`{"commitmentAmount":"0"}`, `{"commitmentAmount":"1.00001"}`, `{"commitmentAmount":"x"}`, `{"commitmentAmount":"5","commitmentQty":"5"}`} {
		if rec := serveBody(t, s, http.MethodPatch, path, body, p, hdr); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", body, rec.Code)
		}
	}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentAmount":"2500.50"}`, p, hdr); rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	r := s.revision
	if r.Commitment == nil || !r.Commitment.Equal(decimal.RequireFromString("2500.5")) || !r.CommitmentIsAmount {
		t.Fatalf("store got %+v", r)
	}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"commitmentQty":"7"}`, p, hdr); rec.Code != http.StatusOK || s.revision.CommitmentIsAmount {
		t.Fatalf("commitmentQty is not flagged as an amount: %d %+v", rec.Code, s.revision)
	}
}
