package api

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// RecordCertificate mirrors the Postgres store: key replay (refused on another
// order), the order checks, and the NCR link of a NOK receipt.
func (f *fakeStore) RecordCertificate(_ context.Context, rec domain.CertRecord, ncr *domain.Nonconformance, key string) (domain.CertRecord, bool, error) {
	if prior, ok := f.certKeys[key]; ok {
		if prior.OrderID != rec.OrderID {
			return prior, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeKeyReuse, "key used on another order")
		}
		return prior, false, nil
	}
	o, ok := f.orders[rec.OrderID]
	if !ok {
		return rec, false, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found")
	}
	if err := domain.CheckCertificateOrder(o, rec.Waiver); err != nil {
		return rec, false, err
	}
	if ncr != nil {
		f.certNCRs = append(f.certNCRs, *ncr)
		rec.NonconformanceID = "ncr-" + rec.ID
	}
	if f.certKeys == nil {
		f.certKeys = map[string]domain.CertRecord{}
	}
	f.certKeys[key] = rec
	f.certs = append(f.certs, rec)
	return rec, true, nil
}

func (f *fakeStore) ListCertificates(_ context.Context, orderID string) ([]domain.CertRecord, error) {
	out := []domain.CertRecord{}
	for _, c := range f.certs {
		if c.OrderID == orderID {
			out = append(out, c)
		}
	}
	return out, nil
}

// RegisterSample mirrors the Postgres store's pooled-sample and lot checks.
func (f *fakeStore) RegisterSample(_ context.Context, in domain.Sample, key string) (domain.Sample, bool, error) {
	if prior, ok := f.sampleKeys[key]; ok {
		return prior, false, nil
	}
	o, ok := f.orders[in.QualityOrderID]
	if !ok {
		return in, false, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found")
	}
	drawn := decimal.Zero
	primaries := map[string]bool{}
	for _, s := range f.samples {
		if s.QualityOrderID != in.QualityOrderID {
			continue
		}
		if s.Kind != domain.SamplePooled {
			drawn = drawn.Add(s.Quantity)
		}
		primaries[s.ID] = s.Kind == domain.SamplePrimary
	}
	for _, p := range in.PrimaryIDs {
		if !primaries[p] {
			return in, false, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSamplePrimary, "not a primary sample of the order")
		}
	}
	if err := domain.CheckSampleOrder(o, in, drawn); err != nil {
		return in, false, err
	}
	if f.sampleKeys == nil {
		f.sampleKeys = map[string]domain.Sample{}
	}
	f.sampleKeys[key] = in
	f.samples = append(f.samples, in)
	return in, true, nil
}

func (f *fakeStore) ListSamples(_ context.Context, filter store.SampleFilter) ([]domain.Sample, error) {
	out := []domain.Sample{}
	for _, s := range f.samples {
		if filter.QualityOrderID != "" && s.QualityOrderID != filter.QualityOrderID {
			continue
		}
		due := s.RetentionExpiresOn != "" && s.RetentionExpiresOn <= filter.AsOf
		if filter.Status != "" && (filter.Status == domain.SampleDueForDisposal) != due {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}
