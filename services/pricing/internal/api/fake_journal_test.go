package api_test

import (
	"context"
	"fmt"
	"net/http"

	"sort"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// fakePeriod is the cumulative state of one agreement in one period.
type fakePeriod = domain.PeriodState

func (f *fakeStore) nextID(kind string) string {
	f.seq++
	return fmt.Sprintf("00000000-0000-0000-%s-%012d", kind, f.seq)
}

func (f *fakeStore) storedRecords() map[string]domain.Record {
	out := make(map[string]domain.Record, len(f.records))
	for id, e := range f.records {
		out[id] = e.Record
	}
	return out
}

func (f *fakeStore) journal(id string) (*store.Journal, error) {
	j, ok := f.journals[id]
	if !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeJournalNotFound, "no price journal "+id)
	}
	return j, nil
}

// apply writes validated after-states, as the Postgres store does in its transaction.
func (f *fakeStore) apply(j *domain.Journal) {
	for i := range j.Lines {
		l := &j.Lines[i]
		if l.After == nil {
			continue
		}
		f.records[l.After.ID] = store.Entry{Record: *l.After, LegalEntityID: j.LegalEntityID}
		l.AfterVersion = l.After.Version
	}
}

func invalidCount(j *domain.Journal) int {
	n := 0
	for _, l := range j.Lines {
		if len(l.Errors) > 0 {
			n++
		}
	}
	return n
}

func (f *fakeStore) CreateJournal(_ context.Context, in store.JournalInput, _ string) (store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := make([]domain.JournalLine, len(in.Lines))
	for i, l := range in.Lines {
		l.No = i + 1
		if l.Op == domain.OpCreate {
			l.RecordID = f.nextID("0004")
		}
		lines[i] = l
	}
	j := &store.Journal{Journal: domain.Journal{ID: f.nextID("0003"), LegalEntityID: in.LegalEntityID, Name: in.Name,
		SmartRound: in.SmartRound, Lines: lines}, Status: domain.JournalOpen, LineCount: len(lines)}
	f.journals[j.ID] = j
	return *j, nil
}

func (f *fakeStore) ListJournals(_ context.Context, flt store.JournalFilter) ([]store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Journal{}
	for _, j := range f.journals {
		if (flt.LegalEntityID == "" || j.LegalEntityID == flt.LegalEntityID) && (flt.Status == "" || j.Status == flt.Status) {
			h := *j
			h.Lines = nil
			out = append(out, h)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out, nil
}

func (f *fakeStore) GetJournal(_ context.Context, id string) (store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.journal(id)
	if err != nil {
		return store.Journal{}, err
	}
	return *j, nil
}

func (f *fakeStore) ValidateJournal(_ context.Context, id string) (store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.journal(id)
	if err != nil {
		return store.Journal{}, err
	}
	if j.Status != domain.JournalOpen && j.Status != domain.JournalValidated {
		return store.Journal{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only OPEN or VALIDATED journals can be validated")
	}
	clean := domain.ValidateJournal(&j.Journal, f.storedRecords(), domain.DefaultSmartBands())
	j.Status = domain.JournalOpen
	if clean {
		j.Status = domain.JournalValidated
	}
	j.ErrorCount = invalidCount(&j.Journal)
	return *j, nil
}

func (f *fakeStore) PostJournal(_ context.Context, id, key string) (store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	j, err := f.journal(id)
	if err != nil {
		return store.Journal{}, err
	}
	if j.Status != domain.JournalValidated {
		return store.Journal{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only VALIDATED journals can post; validate first")
	}
	if !domain.ValidateJournal(&j.Journal, f.storedRecords(), domain.DefaultSmartBands()) {
		return store.Journal{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeJournalInvalid, "Journal has validation errors; nothing was posted")
	}
	f.apply(&j.Journal)
	j.Status = domain.JournalPosted
	return *j, nil
}

func (f *fakeStore) ReverseJournal(_ context.Context, id, key string) (store.Journal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	orig, err := f.journal(id)
	if err != nil {
		return store.Journal{}, err
	}
	if orig.Status != domain.JournalPosted {
		return store.Journal{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only POSTED journals can be reversed")
	}
	rev := domain.Reverse(orig.Journal)
	rev.ID, rev.Name = f.nextID("0003"), "Reversal of "+orig.Name
	if !domain.ValidateJournal(&rev, f.storedRecords(), domain.DefaultSmartBands()) {
		return store.Journal{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeJournalInvalid, "reversal does not validate; nothing was posted")
	}
	f.apply(&rev)
	revJournal := &store.Journal{Journal: rev, Status: domain.JournalPosted, LineCount: len(rev.Lines), ReversesID: id}
	f.journals[rev.ID] = revJournal
	orig.Status, orig.ReversedByID = domain.JournalReversed, rev.ID
	return *revJournal, nil
}

func (f *fakeStore) CreateAgreement(_ context.Context, a domain.RebateAgreement, _ string) (domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.ID = f.nextID("0005")
	f.agreements[a.ID] = a
	return a, nil
}

func (f *fakeStore) GetAgreement(_ context.Context, id string) (domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agreements[id]
	if !ok {
		return a, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id)
	}
	return a, nil
}

func (f *fakeStore) ListAgreements(_ context.Context, legalEntityID string, _ int) ([]domain.RebateAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.RebateAgreement{}
	for _, a := range f.agreements {
		if legalEntityID == "" || a.LegalEntityID == legalEntityID {
			out = append(out, a)
		}
	}
	return out, nil
}

// AccrueInvoiceLine mirrors the store: the period moves to its target and the invoice books the difference.
func (f *fakeStore) AccrueInvoiceLine(_ context.Context, in store.AccrualInput) (store.Accrual, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agreements[in.AgreementID]
	if !ok {
		return store.Accrual{}, false, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+in.AgreementID)
	}
	if in.Currency != a.Currency {
		return store.Accrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice currency does not match the agreement currency")
	}
	dedupe := in.AgreementID + "|" + in.InvoiceLineID
	if acc, ok := f.accruals[dedupe]; ok {
		if !acc.BasisValue.Equal(in.Value) || !acc.BasisQty.Equal(in.Quantity) || !acc.InvoiceDate.Equal(in.InvoiceDate) {
			return store.Accrual{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyUse, "invoice line already accrued differently")
		}
		return acc, false, nil
	}
	key := domain.PeriodKey(a.Period, in.InvoiceDate)
	pk := in.AgreementID + "|" + key
	p := f.periods[pk]
	if p == nil {
		p = &fakePeriod{}
		f.periods[pk] = p
	}
	next, inc := domain.Accrue(a, *p, in.Value, in.Quantity)
	*p = next
	acc := store.Accrual{ID: f.nextID("0006"), AgreementID: a.ID, PeriodKey: key, InvoiceLineID: in.InvoiceLineID,
		InvoiceDate: in.InvoiceDate, BasisValue: in.Value, BasisQty: in.Quantity, Accrued: inc, Currency: a.Currency, Status: "ACCRUED"}
	f.accruals[dedupe] = acc
	return acc, true, nil
}

func (f *fakeStore) ListAccruals(_ context.Context, flt store.AccrualFilter) ([]store.Accrual, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Accrual{}
	for _, a := range f.accruals {
		if (flt.AgreementID == "" || a.AgreementID == flt.AgreementID) && (flt.PeriodKey == "" || a.PeriodKey == flt.PeriodKey) {
			out = append(out, a)
		}
	}
	return out, nil
}
