package api_test

import (
	"context"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// lastAccrual returns the latest invoice date accrued on an agreement (caller holds f.mu).
func (f *fakeStore) lastAccrual(agreementID string) *time.Time {
	var last *time.Time
	for _, acc := range f.accruals {
		if acc.AgreementID == agreementID && (last == nil || acc.InvoiceDate.After(*last)) {
			d := acc.InvoiceDate
			last = &d
		}
	}
	return last
}

func (f *fakeStore) lifecycleAgreement(id string, version int) (domain.RebateAgreement, error) {
	a, ok := f.agreements[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id)
	}
	if a.Version != version {
		return a, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "rebate agreement version does not match")
	}
	if a.Status != domain.AgreementActive {
		return a, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "rebate agreement is "+a.Status)
	}
	return a, nil
}

func (f *fakeStore) ReviseAgreement(_ context.Context, rev store.AgreementRevision, _ string) (domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lifecycleAgreement(rev.ID, rev.Version)
	if err != nil {
		return a, err
	}
	last := f.lastAccrual(rev.ID)
	if rev.Name != nil {
		a.Name = *rev.Name
	}
	if rev.ValidTo != nil {
		a.ValidTo = *rev.ValidTo
	}
	if rev.Tiers != nil {
		if last != nil {
			return a, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementHasAccruals, "tiers cannot change after invoices have accrued")
		}
		a.Tiers = *rev.Tiers
	}
	if last != nil && a.ValidTo.Before(*last) {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementEndsEarly, "validTo is before an accrued invoice")
	}
	a.Version++
	f.agreements[a.ID] = a
	return a, nil
}

func (f *fakeStore) CloseAgreement(_ context.Context, id string, version int, closeDate time.Time, _ string) (domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lifecycleAgreement(id, version)
	if err != nil {
		return a, err
	}
	if closeDate.Before(a.ValidFrom) || closeDate.After(a.ValidTo) {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "closeDate must be within the agreement validity")
	}
	if last := f.lastAccrual(id); last != nil && closeDate.Before(*last) {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAgreementEndsEarly, "closeDate is before an accrued invoice")
	}
	a.Status, a.ValidTo = domain.AgreementClosed, closeDate
	a.Version++
	f.agreements[a.ID] = a
	return a, nil
}

func (f *fakeStore) ReopenAgreement(_ context.Context, id string, version int, validTo time.Time, _ string) (domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agreements[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id)
	}
	if a.Version != version {
		return a, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "rebate agreement version does not match")
	}
	if a.Status != domain.AgreementClosed {
		return a, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "rebate agreement is "+a.Status)
	}
	if !validTo.After(a.ValidTo) {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "validTo must be after the close date")
	}
	a.Status, a.ValidTo = domain.AgreementActive, validTo
	a.Version++
	f.agreements[a.ID] = a
	return a, nil
}
