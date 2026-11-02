package domain

import (
	"context"
	"sync"
)

const (
	PayeeDataClass       = "AP_PAYEE"
	PayeeRetentionMonths = 84
)

type HoldQuery struct {
	LegalEntityID string
	SubjectRef    string
	DataClass     string
	RecordRef     string
}

type LegalHolds interface {
	Covering(ctx context.Context, q HoldQuery) ([]string, error)
}

type FakeLegalHolds struct {
	mu       sync.Mutex
	ByRecord map[string][]string
	Err      error
	Queries  []HoldQuery
}

func (f *FakeLegalHolds) Covering(_ context.Context, q HoldQuery) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Queries = append(f.Queries, q)
	if f.Err != nil {
		return nil, f.Err
	}
	return f.ByRecord[q.RecordRef], nil
}
