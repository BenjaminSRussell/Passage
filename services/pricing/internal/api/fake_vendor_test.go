package api_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// Vendor rebate claims in the fake mirror the store: amount = period accrued - earlier
// claims, PRC-0043 with nothing accrued, PRC-0015 when fully claimed, RC-n per legal entity.

func (f *fakeStore) vendorAgreement(id string) (domain.RebateAgreement, error) {
	a, ok := f.agreements[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id)
	}
	if a.PartyRole != domain.PartyVendor {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodePartyMismatch, "claims apply to VENDOR agreements only")
	}
	return a, nil
}

func (f *fakeStore) position(agreementID, period string) (accrued, claimed decimal.Decimal, claims int, found bool) {
	p, found := f.periods[agreementID+"|"+period]
	if found {
		accrued = p.Accrued
	}
	for _, c := range f.claims {
		if c.AgreementID == agreementID && c.PeriodKey == period {
			claimed, claims = claimed.Add(c.Amount), claims+1
		}
	}
	return accrued, claimed, claims, found
}

func (f *fakeStore) PreviewVendorClaim(_ context.Context, agreementID, period string) (store.VendorClaimPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.vendorAgreement(agreementID)
	if err != nil {
		return store.VendorClaimPreview{}, err
	}
	accrued, claimed, claims, _ := f.position(agreementID, period)
	amount := domain.ClaimAmount(accrued, claimed)
	return store.VendorClaimPreview{AgreementID: a.ID, VendorID: a.VendorID, PeriodKey: period, Accrued: accrued, PriorClaimed: claimed,
		Amount: amount, Currency: a.Currency, SettlementMode: domain.ClaimSettlement(amount, ""), Claims: claims}, nil
}

func (f *fakeStore) CreateVendorClaim(ctx context.Context, in store.VendorClaimInput, _ string) (store.VendorClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.vendorAgreement(in.AgreementID)
	if err != nil {
		return store.VendorClaim{}, err
	}
	accrued, claimed, claims, found := f.position(a.ID, in.PeriodKey)
	amount := domain.ClaimAmount(accrued, claimed)
	if !found || (amount.IsZero() && claims == 0) {
		return store.VendorClaim{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNothingToClaim, "nothing accrued")
	}
	if amount.IsZero() {
		return store.VendorClaim{}, httpx.NewProblem(http.StatusConflict, store.CodeClaimAlreadyProcessed, "already processed")
	}
	n := 1
	for _, c := range f.claims {
		if c.LegalEntityID == a.LegalEntityID {
			n++
		}
	}
	p, _ := tenant.FromContext(ctx)
	c := store.VendorClaim{ID: f.nextID("0009"), Number: fmt.Sprintf("RC-%06d", n), LegalEntityID: a.LegalEntityID, AgreementID: a.ID,
		VendorID: a.VendorID, PeriodKey: in.PeriodKey, AccruedTotal: accrued, PriorClaimed: claimed, Amount: amount, Currency: a.Currency,
		SettlementMode: domain.ClaimSettlement(amount, in.SettlementMode), Status: domain.ClaimOpen, Version: 1, CreatedBy: p.UserID,
		CreatedAt: time.Date(2027, 4, 1, 8, 0, 0, 0, time.UTC)}
	f.claims = append(f.claims, c)
	return c, nil
}

func (f *fakeStore) GetVendorClaim(_ context.Context, id string) (store.VendorClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.claims {
		if c.ID == id {
			return c, nil
		}
	}
	return store.VendorClaim{}, httpx.NewProblem(http.StatusNotFound, store.CodeClaimNotFound, "no vendor rebate claim "+id)
}

func (f *fakeStore) ListVendorClaims(_ context.Context, flt store.ClaimFilter) ([]store.VendorClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.VendorClaim{}
	for _, c := range f.claims {
		if (flt.AgreementID == "" || c.AgreementID == flt.AgreementID) && (flt.VendorID == "" || c.VendorID == flt.VendorID) &&
			(flt.PeriodKey == "" || c.PeriodKey == flt.PeriodKey) && (flt.Status == "" || c.Status == flt.Status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) ApproveVendorClaim(ctx context.Context, id string, version int, _ string) (store.VendorClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, _ := tenant.FromContext(ctx)
	for i, c := range f.claims {
		if c.ID != id {
			continue
		}
		switch {
		case c.Version != version:
			return c, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "claim version does not match")
		case c.Status != domain.ClaimOpen:
			return c, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only OPEN claims can be approved")
		case p.UserID != "" && p.UserID == c.CreatedBy:
			return c, httpx.NewProblem(http.StatusForbidden, store.CodeClaimFourEyes, "the creator cannot approve")
		}
		at := time.Date(2027, 4, 2, 8, 0, 0, 0, time.UTC)
		c.Status, c.Version, c.ApprovedBy, c.ApprovedAt = domain.ClaimApproved, c.Version+1, p.UserID, &at
		f.claims[i] = c
		return c, nil
	}
	return store.VendorClaim{}, httpx.NewProblem(http.StatusNotFound, store.CodeClaimNotFound, "no vendor rebate claim "+id)
}
