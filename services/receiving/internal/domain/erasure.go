package domain

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	PayeeDataClass       = "AP_PAYEE"
	PayeeRetentionMonths = 84
)

var statutoryFloorMonths = map[string]int{"DE": 120, "FR": 120}

func PayeeRetentionEnd(entityCountry string, lastActivity time.Time) (time.Time, bool) {
	country := strings.ToUpper(strings.TrimSpace(entityCountry))
	if len(country) != 2 || statutoryFloorMonths[country] > PayeeRetentionMonths {
		return time.Time{}, false
	}
	return lastActivity.UTC().AddDate(0, PayeeRetentionMonths, 0), true
}

func ParseLegalEntityCountries(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		rawID, country, ok := strings.Cut(pair, "=")
		id, valid := ids.Canonical(strings.TrimSpace(rawID))
		country = strings.ToUpper(strings.TrimSpace(country))
		if !ok || !valid || len(country) != 2 {
			return nil, fmt.Errorf("LEGAL_ENTITY_COUNTRIES entry %q must be <legal entity uuid>=<ISO country>", pair)
		}
		out[id] = country
	}
	return out, nil
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
