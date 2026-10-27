package api_test

import (
	"context"
	"time"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// ExpireRecords mirrors the Postgres rule: ACTIVE records of the legal entity whose
// validity ended before asOf become EXPIRED with the next version; nothing else changes.
func (f *fakeStore) ExpireRecords(_ context.Context, legalEntityID string, asOf time.Time) ([]store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Entry
	for id, e := range f.records {
		if e.LegalEntityID == legalEntityID && e.Status == domain.StatusActive && e.ValidTo.Before(asOf) {
			e.Status, e.Version = domain.StatusExpired, e.Version+1
			f.records[id] = e
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) SettlementTotals(context.Context, string, time.Time, time.Time) ([]domain.SettlementRow, error) {
	return []domain.SettlementRow{}, nil
}

func (f *fakeStore) WorkspaceSummary(_ context.Context, _ string, asOf time.Time) (store.WorkspaceSummary, error) {
	return store.WorkspaceSummary{Month: asOf.Format("2006-01"), AccrualsThisMonth: []store.AccrualTotal{}}, nil
}

// opsFake returns canned tiles and settlement rows and records what the handlers asked for.
type opsFake struct {
	*fakeStore
	rows        []domain.SettlementRow
	settleLE    string
	settleFrom  time.Time
	settleTo    time.Time
	summary     store.WorkspaceSummary
	summaryLE   string
	summaryAsOf time.Time
}

func (s *opsFake) SettlementTotals(_ context.Context, le string, from, to time.Time) ([]domain.SettlementRow, error) {
	s.settleLE, s.settleFrom, s.settleTo = le, from, to
	return s.rows, nil
}

func (s *opsFake) WorkspaceSummary(_ context.Context, le string, asOf time.Time) (store.WorkspaceSummary, error) {
	s.summaryLE, s.summaryAsOf = le, asOf
	return s.summary, nil
}
