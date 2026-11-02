package domain

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	PayeeDataClass       = "AP_PAYEE"
	PayeeRetentionMonths = 84
)

var statutoryFloorMonths = map[string]int{"DE": 120, "FR": 120}

func PayeeRetentionEnd(country string, lastActivity time.Time) (time.Time, bool) {
	if statutoryFloorMonths[strings.ToUpper(strings.TrimSpace(country))] > PayeeRetentionMonths {
		return time.Time{}, false
	}
	return lastActivity.UTC().AddDate(0, PayeeRetentionMonths, 0), true
}

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
