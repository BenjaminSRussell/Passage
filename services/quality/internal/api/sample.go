package api

import (
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// registerSample registers a physical sample of a quality order (FR-33.20). The
// sample takes the order's unit; a retention sample expires the given years
// (default 1) after the batch expiry.
func (a *API) registerSample(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req domain.SampleInput
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.ValidateSample(req); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	order, err := a.store.GetOrder(r.Context(), req.QualityOrderID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now().UTC()
	smp, err := domain.NewSample(ids.New(), req, order.UOM, p.UserID, now)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	saved, inserted, err := a.store.RegisterSample(r.Context(), smp, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if inserted {
		a.samplesRegistered.Add(1)
	}
	httpx.JSON(w, statusFor(inserted), saved.WithStatus(now))
}

// listSamples is the sample list: ?qualityOrderId=&status=&limit=. The status is
// judged as of today (UTC), so DUE_FOR_DISPOSAL lists the retention samples whose
// expiry has arrived (AC-33.18).
func (a *API) listSamples(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	f := store.SampleFilter{QualityOrderID: q.Get("qualityOrderId"), Status: q.Get("status"), AsOf: now.Format(domain.DateLayout)}
	fields := map[string]string{}
	if f.QualityOrderID != "" && !ids.Valid(f.QualityOrderID) {
		fields["qualityOrderId"] = "must be a UUID"
	}
	if f.Status != "" && !domain.ValidSampleStatus(f.Status) {
		fields["status"] = "must be ACTIVE or DUE_FOR_DISPOSAL"
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	f.Limit = limit
	samples, err := a.store.ListSamples(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range samples {
		samples[i] = samples[i].WithStatus(now)
	}
	httpx.JSON(w, http.StatusOK, list[domain.Sample]{Items: samples})
}
