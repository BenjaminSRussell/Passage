package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

type fakePos struct {
	value, qty, accrued, shortfall decimal.Decimal
	closed                         bool
}

// brokerState is the fake's broker contracts, royalty agreements, accruals and claims. It
// applies the store's rules through the domain functions.
type brokerState struct {
	brokers  map[string]domain.BrokerContract
	royalty  map[string]domain.RoyaltyAgreement
	accruals []store.ContractAccrual
	periods  map[string]*fakePos
	claims   []store.ContractClaim
	replays  map[string]string
}

func (f *fakeStore) brokerData() *brokerState {
	if f.brk.brokers == nil {
		f.brk = brokerState{brokers: map[string]domain.BrokerContract{}, royalty: map[string]domain.RoyaltyAgreement{},
			periods: map[string]*fakePos{}, replays: map[string]string{}}
	}
	return &f.brk
}

func fakeID(n int) string { return "00000000-0000-4000-8000-" + padSeq(n) }

func (f *fakeStore) CreateBrokerContract(_ context.Context, in domain.BrokerContract, key string) (domain.BrokerContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	if id, ok := st.replays[key]; ok && key != "" {
		return st.brokers[id], nil
	}
	f.seq++
	in.ID, in.Version, in.Status = fakeID(f.seq+700), 1, domain.AgreementActive
	st.brokers[in.ID] = in
	if key != "" {
		st.replays[key] = in.ID
	}
	return in, nil
}

func (f *fakeStore) GetBrokerContract(_ context.Context, id string) (domain.BrokerContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.brokerData().brokers[id]
	if !ok {
		return c, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no broker contract "+id)
	}
	return c, nil
}

func (f *fakeStore) ListBrokerContracts(_ context.Context, fl store.ContractFilter) ([]domain.BrokerContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.BrokerContract{}
	for _, c := range f.brokerData().brokers {
		if (fl.LegalEntityID == "" || c.LegalEntityID == fl.LegalEntityID) && (fl.VendorID == "" || c.BrokerVendorID == fl.VendorID) &&
			(fl.Status == "" || c.Status == fl.Status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) CloseBrokerContract(_ context.Context, id string, version int, _ string) (domain.BrokerContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	c, ok := st.brokers[id]
	if !ok {
		return c, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no broker contract "+id)
	}
	if c.Version != version {
		return c, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "version")
	}
	if c.Status != domain.AgreementActive {
		return c, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "closed")
	}
	c.Status, c.Version = domain.AgreementClosed, c.Version+1
	st.brokers[id] = c
	return c, nil
}

func (f *fakeStore) CreateRoyaltyAgreement(_ context.Context, in domain.RoyaltyAgreement, key string) (domain.RoyaltyAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	if id, ok := st.replays[key]; ok && key != "" {
		return st.royalty[id], nil
	}
	f.seq++
	in.ID, in.Version, in.Status = fakeID(f.seq+700), 1, domain.AgreementActive
	st.royalty[in.ID] = in
	if key != "" {
		st.replays[key] = in.ID
	}
	return in, nil
}

func (f *fakeStore) GetRoyaltyAgreement(_ context.Context, id string) (domain.RoyaltyAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.brokerData().royalty[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no royalty agreement "+id)
	}
	return a, nil
}

func (f *fakeStore) ListRoyaltyAgreements(_ context.Context, fl store.ContractFilter) ([]domain.RoyaltyAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.RoyaltyAgreement{}
	for _, a := range f.brokerData().royalty {
		if (fl.LegalEntityID == "" || a.LegalEntityID == fl.LegalEntityID) && (fl.VendorID == "" || a.LicensorVendorID == fl.VendorID) &&
			(fl.Status == "" || a.Status == fl.Status) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) CloseRoyaltyAgreement(_ context.Context, id string, version int, _ string) (domain.RoyaltyAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	a, ok := st.royalty[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no royalty agreement "+id)
	}
	if a.Version != version {
		return a, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "version")
	}
	a.Status, a.Version = domain.AgreementClosed, a.Version+1
	st.royalty[id] = a
	return a, nil
}

func (st *brokerState) pos(kind, id, period string) *fakePos {
	k := kind + "|" + id + "|" + period
	if st.periods[k] == nil {
		st.periods[k] = &fakePos{}
	}
	return st.periods[k]
}

func (f *fakeStore) AccrueContractLine(_ context.Context, in store.ContractAccrualInput) (store.ContractAccrual, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	l := in.Line
	for _, a := range st.accruals {
		if a.ContractKind == in.Kind && a.ContractID == in.ContractID && a.LineKey == l.InvoiceLineID {
			return a, false, nil
		}
	}
	var amount decimal.Decimal
	period := domain.PeriodMonth
	switch in.Kind {
	case domain.ContractBroker:
		c, ok := st.brokers[in.ContractID]
		if !ok {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no broker contract")
		}
		if c.Status != domain.AgreementActive {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusConflict, domain.CodeContractClosed, "closed")
		}
		if !domain.BrokerApplies(c, l) {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "out of scope")
		}
		amount = domain.BrokerAmount(c, l)
		if l.IsCredit() {
			var orig *store.ContractAccrual
			credBasis, credAmt := decimal.Zero, decimal.Zero
			for i, a := range st.accruals {
				if a.ContractID == in.ContractID && a.LineKey == l.ReversesInvoiceLineID && a.Kind == domain.AccrualLine {
					orig = &st.accruals[i]
				}
				if a.ContractID == in.ContractID && a.ReversesLineKey == l.ReversesInvoiceLineID {
					credBasis, credAmt = credBasis.Add(a.BasisValue.Neg()), credAmt.Add(a.Amount.Neg())
				}
			}
			if orig == nil {
				return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceedsOriginal, "never accrued")
			}
			var derr *domain.Error
			amount, derr = domain.BrokerCredit(orig.BasisValue, orig.Amount, credBasis, credAmt, domain.BrokerBasis(c, l), c.Currency)
			if derr != nil {
				return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, derr.Code, derr.Detail)
			}
		}
	default:
		a, ok := st.royalty[in.ContractID]
		if !ok {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no royalty agreement")
		}
		if a.Status != domain.AgreementActive {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusConflict, domain.CodeContractClosed, "closed")
		}
		if !domain.RoyaltyApplies(a, l) {
			return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "out of scope")
		}
		period = a.Period
	}
	key := domain.PeriodKey(period, l.InvoiceDate)
	p := st.pos(in.Kind, in.ContractID, key)
	if p.closed {
		return store.ContractAccrual{}, false, httpx.NewProblem(http.StatusConflict, domain.CodePeriodClosed, "closed period")
	}
	value, qty, kind := l.NetAmount, l.Quantity, domain.AccrualLine
	if l.IsCredit() {
		value, qty, kind = value.Neg(), qty.Neg(), domain.AccrualCredit
	}
	if in.Kind == domain.ContractRoyalty {
		next, inc := domain.Accrue(domain.RoyaltyRebateView(st.royalty[in.ContractID]), domain.PeriodState{Value: p.value, Qty: p.qty, Accrued: p.accrued}, value, qty)
		p.value, p.qty, p.accrued, amount = next.Value, next.Qty, next.Accrued, inc
	} else {
		p.value, p.qty, p.accrued = p.value.Add(value), p.qty.Add(qty), p.accrued.Add(amount)
	}
	f.seq++
	acc := store.ContractAccrual{ID: fakeID(f.seq + 900), ContractKind: in.Kind, ContractID: in.ContractID, PeriodKey: key, LineKey: l.InvoiceLineID, Kind: kind,
		InvoiceDate: l.InvoiceDate, BasisValue: value, BasisQty: qty, Amount: amount, Currency: l.Currency, ReversesLineKey: l.ReversesInvoiceLineID}
	st.accruals = append(st.accruals, acc)
	return acc, true, nil
}

func (f *fakeStore) ListContractAccruals(_ context.Context, fl store.ContractAccrualFilter) ([]store.ContractAccrual, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ContractAccrual{}
	for _, a := range f.brokerData().accruals {
		if a.ContractKind == fl.Kind && a.ContractID == fl.ContractID && (fl.PeriodKey == "" || a.PeriodKey == fl.PeriodKey) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) CloseRoyaltyPeriod(_ context.Context, id, period string) (store.RoyaltyClose, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	a, ok := st.royalty[id]
	if !ok {
		return store.RoyaltyClose{}, httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no royalty agreement "+id)
	}
	if !domain.PeriodMatches(a.Period, period) || !domain.PeriodOverlapsValidity(period, a.ValidFrom, a.ValidTo) {
		return store.RoyaltyClose{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "bad period")
	}
	p := st.pos(domain.ContractRoyalty, id, period)
	out := store.RoyaltyClose{AgreementID: id, PeriodKey: period, Accrued: p.accrued, Minimum: a.MinimumRoyalty, Shortfall: p.shortfall, Currency: a.Currency, AlreadyClosed: p.closed}
	if p.closed {
		return out, nil
	}
	p.closed, p.shortfall = true, domain.MinimumShortfall(a.MinimumRoyalty, p.accrued)
	out.Shortfall = p.shortfall
	if p.shortfall.IsPositive() {
		f.seq++
		_, last, _ := domain.PeriodBounds(period)
		acc := store.ContractAccrual{ID: fakeID(f.seq + 900), ContractKind: domain.ContractRoyalty, ContractID: id, PeriodKey: period, LineKey: "MIN:" + period,
			Kind: domain.AccrualShortfall, InvoiceDate: last, Amount: p.shortfall, Currency: a.Currency}
		st.accruals = append(st.accruals, acc)
		out.Accrual = &acc
	}
	return out, nil
}

func (f *fakeStore) contractRef(kind, id string) (vendor, currency string, err error) {
	st := f.brokerData()
	if kind == domain.ContractBroker {
		if c, ok := st.brokers[id]; ok {
			return c.BrokerVendorID, c.Currency, nil
		}
	} else if a, ok := st.royalty[id]; ok {
		return a.LicensorVendorID, a.Currency, nil
	}
	return "", "", httpx.NewProblem(http.StatusNotFound, domain.CodeContractNotFound, "no contract "+id)
}

func (f *fakeStore) claimPosition(kind, id, period string) store.ContractClaimPreview {
	st := f.brokerData()
	vendor, cur, _ := f.contractRef(kind, id)
	p := st.pos(kind, id, period)
	prior, claims := decimal.Zero, 0
	for _, c := range st.claims {
		if c.ContractKind == kind && c.ContractID == id && c.PeriodKey == period {
			prior, claims = prior.Add(c.Amount), claims+1
		}
	}
	return store.ContractClaimPreview{ContractKind: kind, ContractID: id, VendorID: vendor, PeriodKey: period, Accrued: p.accrued, Shortfall: p.shortfall,
		PriorClaimed: prior, Amount: domain.ClaimAmount(p.accrued.Add(p.shortfall), prior), Currency: cur, Claims: claims, Closed: p.closed}
}

func (f *fakeStore) PreviewContractClaim(_ context.Context, kind, id, period string) (store.ContractClaimPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, _, err := f.contractRef(kind, id); err != nil {
		return store.ContractClaimPreview{}, err
	}
	return f.claimPosition(kind, id, period), nil
}

func (f *fakeStore) CreateContractClaim(ctx context.Context, kind, id, period, key string) (store.ContractClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	if _, _, err := f.contractRef(kind, id); err != nil {
		return store.ContractClaim{}, err
	}
	pos := f.claimPosition(kind, id, period)
	if pos.Amount.IsZero() && pos.Claims == 0 {
		return store.ContractClaim{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNothingToClaim, "nothing accrued")
	}
	if kind == domain.ContractRoyalty && !pos.Closed {
		return store.ContractClaim{}, httpx.NewProblem(http.StatusConflict, store.CodeContractPeriodOpen, "close the period first")
	}
	if pos.Amount.IsZero() {
		return store.ContractClaim{}, httpx.NewProblem(http.StatusConflict, store.CodeClaimAlreadyProcessed, "fully claimed")
	}
	prefix, n := "BC", 1
	if kind == domain.ContractRoyalty {
		prefix = "RY"
	}
	for _, c := range st.claims {
		if c.ContractKind == kind {
			n++
		}
	}
	f.seq++
	p, _ := tenant.FromContext(ctx)
	c := store.ContractClaim{ID: fakeID(f.seq + 1100), Number: fmt.Sprintf("%s-%06d", prefix, n), LegalEntityID: legalEntity, ContractKind: kind, ContractID: id,
		VendorID: pos.VendorID, PeriodKey: period, AccruedTotal: pos.Accrued.Add(pos.Shortfall), PriorClaimed: pos.PriorClaimed, Amount: pos.Amount,
		Currency: pos.Currency, Status: domain.ClaimOpen, Version: 1, CreatedBy: p.UserID}
	st.claims = append(st.claims, c)
	return c, nil
}

func (f *fakeStore) GetContractClaim(_ context.Context, id string) (store.ContractClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.brokerData().claims {
		if c.ID == id {
			return c, nil
		}
	}
	return store.ContractClaim{}, httpx.NewProblem(http.StatusNotFound, store.CodeClaimNotFound, "no claim "+id)
}

func (f *fakeStore) ListContractClaims(_ context.Context, fl store.ContractClaimFilter) ([]store.ContractClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ContractClaim{}
	for _, c := range f.brokerData().claims {
		if (fl.Kind == "" || c.ContractKind == fl.Kind) && (fl.ContractID == "" || c.ContractID == fl.ContractID) &&
			(fl.PeriodKey == "" || c.PeriodKey == fl.PeriodKey) && (fl.Status == "" || c.Status == fl.Status) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) ApproveContractClaim(ctx context.Context, id string, version int, _ string) (store.ContractClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.brokerData()
	p, _ := tenant.FromContext(ctx)
	for i, c := range st.claims {
		if c.ID != id {
			continue
		}
		switch {
		case c.Version != version:
			return c, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "version")
		case c.Status != domain.ClaimOpen:
			return c, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "not open")
		case p.UserID == "" || p.UserID == c.CreatedBy:
			return c, httpx.NewProblem(http.StatusForbidden, store.CodeClaimFourEyes, "four eyes")
		}
		c.Status, c.Version, c.ApprovedBy = domain.ClaimApproved, c.Version+1, p.UserID
		st.claims[i] = c
		return c, nil
	}
	return store.ContractClaim{}, httpx.NewProblem(http.StatusNotFound, store.CodeClaimNotFound, "no claim "+strconv.Quote(id))
}
